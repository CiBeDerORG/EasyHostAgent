// Package taskstore provides durable, idempotent Agent-local task records.
// Requests (including secret parameters) are encrypted at rest. The store
// never executes work itself; the task runner owns execution and cancellation.
package taskstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	SchemaVersion       = 1
	MaxTaskRequestBytes = 8 << 20
	MaxLogAppendBytes   = 1 << 20
	MaxLogChunkBytes    = 256 << 10
)

var (
	ErrConflict          = errors.New("task ID already exists with different request")
	ErrNotFound          = errors.New("task not found")
	ErrInvalidRequest    = errors.New("invalid task request")
	ErrInvalidTransition = errors.New("invalid task state transition")
	ErrInvalidOffset     = errors.New("invalid task log offset")
	ErrTerminalTask      = errors.New("task is already terminal")
	uuidPattern          = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
	sha256Pattern        = regexp.MustCompile("^[0-9a-f]{64}$")
	sha1Pattern          = regexp.MustCompile("^[0-9a-f]{40}$")
	appIDPattern         = regexp.MustCompile("^[a-z][a-z0-9]*(-[a-z0-9]+)*$")
)

type Status string

const (
	StatusAccepted    Status = "accepted"
	StatusRunning     Status = "running"
	StatusCancelling  Status = "cancelling"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled || s == StatusInterrupted
}

// TaskStart mirrors the immutable, client-generated request used for
// idempotency. Secrets are encrypted and never copied into public Task values.
type TaskStart struct {
	TaskID          string            `json:"task_id"`
	Kind            string            `json:"kind"`
	Action          string            `json:"action"`
	ApplicationID   string            `json:"application_id,omitempty"`
	ResourceID      string            `json:"resource_id,omitempty"`
	BundleSHA256    string            `json:"bundle_sha256,omitempty"`
	CatalogRevision string            `json:"catalog_revision,omitempty"`
	Variant         string            `json:"variant,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	Secrets         map[string]string `json:"secrets,omitempty"`
	Options         *TaskOptions      `json:"options,omitempty"`
}

type TaskOptions struct {
	RetainData        *bool `json:"retain_data,omitempty"`
	ConfirmDeleteData bool  `json:"confirm_delete_data,omitempty"`
	ReuseExistingData bool  `json:"reuse_existing_data,omitempty"`
}

type Task struct {
	AgentID       string         `json:"agent_id"`
	TaskID        string         `json:"task_id"`
	Kind          string         `json:"kind"`
	Action        string         `json:"action"`
	ApplicationID string         `json:"application_id,omitempty"`
	ResourceID    string         `json:"resource_id,omitempty"`
	Status        Status         `json:"status"`
	Message       string         `json:"message,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	LogOffset     int64          `json:"log_offset"`
	Result        map[string]any `json:"result,omitempty"`

	createdAtNS int64
}

type Page struct {
	AgentID    string  `json:"agent_id"`
	Items      []Task  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type LogChunk struct {
	AgentID    string `json:"agent_id"`
	TaskID     string `json:"task_id"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Text       string `json:"text"`
	EOF        bool   `json:"eof"`
	Truncated  bool   `json:"truncated"`
}

type Store struct {
	db      *sql.DB
	key     []byte
	agentID string
	now     func() time.Time
}

func New(dataDir, agentID string) (*Store, error) {
	if !uuidPattern.MatchString(agentID) {
		return nil, fmt.Errorf("%w: invalid Agent ID", ErrInvalidRequest)
	}
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" {
		return nil, fmt.Errorf("%w: task data directory must be an absolute non-root path", ErrInvalidRequest)
	}
	dirInfo, err := os.Lstat(dataDir)
	if err != nil {
		return nil, fmt.Errorf("inspect task data directory: %w", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm()&0077 != 0 {
		return nil, errors.New("task data directory must be a real private directory (0700)")
	}

	key, err := loadOrCreateKey(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load task encryption key: %w", err)
	}
	dbPath := filepath.Join(dataDir, "tasks.sqlite3")
	if err := ensureDatabaseFile(dbPath); err != nil {
		return nil, fmt.Errorf("prepare task database: %w", err)
	}
	u := &url.URL{Scheme: "file", Path: dbPath}
	query := url.Values{}
	query.Set("mode", "rwc")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "journal_mode(DELETE)")
	u.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open task database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	fail := func(e error) (*Store, error) {
		_ = db.Close()
		return nil, e
	}
	if err := db.Ping(); err != nil {
		return fail(fmt.Errorf("connect task database: %w", err))
	}
	if err := initializeSchema(db); err != nil {
		return fail(fmt.Errorf("initialize task database: %w", err))
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		return fail(fmt.Errorf("inspect task database: %w", err))
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fail(errors.New("task database must be a private regular file (0600)"))
	}

	store := &Store{db: db, key: key, agentID: agentID, now: time.Now}
	if err := store.markInterruptedOnStartup(); err != nil {
		return fail(fmt.Errorf("mark interrupted tasks: %w", err))
	}
	return store, nil
}

func initializeSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("task database schema %d is newer than supported schema %d", version, SchemaVersion)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			task_id TEXT PRIMARY KEY,
			request_sha256 TEXT NOT NULL CHECK(length(request_sha256) = 64),
			request_ciphertext BLOB NOT NULL,
			kind TEXT NOT NULL,
			action TEXT NOT NULL,
			application_id TEXT NOT NULL DEFAULT '',
			resource_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('accepted','running','cancelling','succeeded','failed','cancelled','interrupted')),
			message TEXT NOT NULL DEFAULT '',
			exit_code INTEGER NULL,
			created_at_ns INTEGER NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			finished_at_ns INTEGER NULL,
			log_offset INTEGER NOT NULL DEFAULT 0 CHECK(log_offset >= 0),
			result_json TEXT NOT NULL DEFAULT '{}',
			pending_log BLOB NOT NULL DEFAULT X''
		)`,
		`CREATE TABLE IF NOT EXISTS task_logs (
			task_id TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
			offset INTEGER NOT NULL CHECK(offset >= 0),
			data BLOB NOT NULL,
			PRIMARY KEY(task_id, offset)
		)`,
		`CREATE INDEX IF NOT EXISTS tasks_created_order ON tasks(created_at_ns DESC, task_id DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if version < SchemaVersion {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("task database must be a private regular file (0600)")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		return ensureDatabaseFile(path)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

func loadOrCreateKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "tasks.key")
	if key, err := readKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".tasks-key-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readKey(path)
		}
		return nil, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return key, nil
}

func readKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("task encryption key must be a private regular file (0600)")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("task encryption key has an invalid length")
	}
	return key, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Create(request TaskStart) (Task, bool, error) {
	request, payload, digest, err := normalizeRequest(request)
	if err != nil {
		return Task{}, false, err
	}
	ciphertext, err := s.seal("task-request:"+request.TaskID, payload)
	if err != nil {
		return Task{}, false, err
	}
	now := s.now().UTC().UnixNano()
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, false, err
	}
	result, err := tx.Exec(`INSERT INTO tasks (
		task_id, request_sha256, request_ciphertext, kind, action, application_id,
		resource_id, status, message, created_at_ns, updated_at_ns, log_offset,
		result_json, pending_log
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, 0, '{}', X'')
	ON CONFLICT(task_id) DO NOTHING`,
		request.TaskID, digest, ciphertext, request.Kind, request.Action,
		request.ApplicationID, request.ResourceID, StatusAccepted, now, now)
	if err != nil {
		_ = tx.Rollback()
		return Task{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return Task{}, false, err
	}
	if inserted == 0 {
		var existingDigest string
		err := tx.QueryRow("SELECT request_sha256 FROM tasks WHERE task_id = ?", request.TaskID).Scan(&existingDigest)
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false, ErrNotFound
		}
		if err != nil {
			return Task{}, false, err
		}
		if existingDigest != digest {
			return Task{}, false, ErrConflict
		}
		task, err := s.Get(request.TaskID)
		return task, true, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, err
	}
	task, err := s.Get(request.TaskID)
	return task, false, err
}

func normalizeRequest(request TaskStart) (TaskStart, []byte, string, error) {
	if !uuidPattern.MatchString(request.TaskID) {
		return TaskStart{}, nil, "", fmt.Errorf("%w: task_id must be a UUID", ErrInvalidRequest)
	}
	if request.Kind != "application" && request.Kind != "docker_resource" && request.Kind != "host" {
		return TaskStart{}, nil, "", fmt.Errorf("%w: unsupported task kind", ErrInvalidRequest)
	}
	if !validAction(request.Kind, request.Action) {
		return TaskStart{}, nil, "", fmt.Errorf("%w: action is not allowed for this task kind", ErrInvalidRequest)
	}
	switch request.Kind {
	case "application":
		if !appIDPattern.MatchString(request.ApplicationID) || request.ResourceID != "" {
			return TaskStart{}, nil, "", fmt.Errorf("%w: application task requires application_id only", ErrInvalidRequest)
		}
	case "docker_resource":
		if request.ResourceID == "" || len(request.ResourceID) > 512 || request.ApplicationID != "" {
			return TaskStart{}, nil, "", fmt.Errorf("%w: Docker resource task requires resource_id only", ErrInvalidRequest)
		}
	case "host":
		if request.ApplicationID != "" || request.ResourceID != "" {
			return TaskStart{}, nil, "", fmt.Errorf("%w: host task cannot target an application or Docker resource", ErrInvalidRequest)
		}
	}
	if request.BundleSHA256 != "" && !sha256Pattern.MatchString(request.BundleSHA256) {
		return TaskStart{}, nil, "", fmt.Errorf("%w: bundle_sha256 must be a lowercase SHA-256 digest", ErrInvalidRequest)
	}
	if request.CatalogRevision != "" && !sha1Pattern.MatchString(request.CatalogRevision) {
		return TaskStart{}, nil, "", fmt.Errorf("%w: catalog_revision must be a 40-character Git revision", ErrInvalidRequest)
	}
	if len(request.Variant) > 128 {
		return TaskStart{}, nil, "", fmt.Errorf("%w: variant is too long", ErrInvalidRequest)
	}
	for _, values := range []map[string]string{request.Parameters, request.Secrets} {
		if len(values) > 512 {
			return TaskStart{}, nil, "", fmt.Errorf("%w: too many task parameters", ErrInvalidRequest)
		}
		for key, value := range values {
			if strings.TrimSpace(key) == "" || len(key) > 256 || len(value) > MaxTaskRequestBytes {
				return TaskStart{}, nil, "", fmt.Errorf("%w: invalid task parameter", ErrInvalidRequest)
			}
		}
	}
	if request.Options == nil {
		request.Options = &TaskOptions{}
	}
	if request.Options.RetainData == nil {
		retain := true
		request.Options.RetainData = &retain
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return TaskStart{}, nil, "", fmt.Errorf("%w: encode task request", ErrInvalidRequest)
	}
	if len(payload) > MaxTaskRequestBytes {
		return TaskStart{}, nil, "", fmt.Errorf("%w: task request is too large", ErrInvalidRequest)
	}
	sum := sha256.Sum256(payload)
	return request, payload, hex.EncodeToString(sum[:]), nil
}

func validAction(kind, action string) bool {
	switch kind {
	case "application":
		switch action {
		case "install", "update", "uninstall", "configure", "adopt", "start", "stop", "restart":
			return true
		}
	case "docker_resource":
		return action == "start" || action == "stop" || action == "restart"
	case "host":
		return action == "install_docker" || action == "upgrade_agent"
	}
	return false
}

func (s *Store) Get(taskID string) (Task, error) {
	if !uuidPattern.MatchString(taskID) {
		return Task{}, fmt.Errorf("%w: invalid task ID", ErrInvalidRequest)
	}
	task, err := scanTask(s.db.QueryRow(`SELECT task_id, kind, action, application_id, resource_id,
		status, message, exit_code, created_at_ns, updated_at_ns, finished_at_ns,
		log_offset, result_json FROM tasks WHERE task_id = ?`, taskID), s.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// GetRequest decrypts the immutable request for the trusted local task runner.
// Callers must never serialize its Secrets field into API responses or logs.
func (s *Store) GetRequest(taskID string) (TaskStart, error) {
	if !uuidPattern.MatchString(taskID) {
		return TaskStart{}, fmt.Errorf("%w: invalid task ID", ErrInvalidRequest)
	}
	var ciphertext []byte
	var expectedDigest string
	if err := s.db.QueryRow("SELECT request_ciphertext, request_sha256 FROM tasks WHERE task_id = ?", taskID).Scan(&ciphertext, &expectedDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TaskStart{}, ErrNotFound
		}
		return TaskStart{}, err
	}
	request, err := s.decodeRequest(taskID, ciphertext)
	if err != nil {
		return TaskStart{}, err
	}
	_, _, digest, err := normalizeRequest(request)
	if err != nil || digest != expectedDigest {
		return TaskStart{}, errors.New("stored task request integrity check failed")
	}
	return request, nil
}

func (s *Store) List(limit int, cursor string) (Page, error) {
	if limit < 1 || limit > 100 {
		return Page{}, fmt.Errorf("%w: list limit must be between 1 and 100", ErrInvalidRequest)
	}
	var cursorTime int64
	var cursorID string
	if cursor != "" {
		var err error
		cursorTime, cursorID, err = decodeCursor(cursor)
		if err != nil {
			return Page{}, fmt.Errorf("%w: invalid task cursor", ErrInvalidRequest)
		}
	}
	query := `SELECT task_id, kind, action, application_id, resource_id, status, message,
		exit_code, created_at_ns, updated_at_ns, finished_at_ns, log_offset, result_json
		FROM tasks`
	args := []any{}
	if cursor != "" {
		query += ` WHERE created_at_ns < ? OR (created_at_ns = ? AND task_id < ?)`
		args = append(args, cursorTime, cursorTime, cursorID)
	}
	query += ` ORDER BY created_at_ns DESC, task_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := make([]Task, 0, limit+1)
	for rows.Next() {
		task, err := scanTask(rows, s.agentID)
		if err != nil {
			return Page{}, err
		}
		items = append(items, task)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	page := Page{AgentID: s.agentID, Items: items}
	if len(items) > limit {
		items = items[:limit]
		page.Items = items
		next := encodeCursor(items[len(items)-1].createdAtNS, items[len(items)-1].TaskID)
		page.NextCursor = &next
	}
	return page, nil
}

func (s *Store) Transition(taskID string, next Status, message string, exitCode *int, result map[string]any) (Task, error) {
	if !uuidPattern.MatchString(taskID) || !validStatus(next) || len(message) > 4096 {
		return Task{}, fmt.Errorf("%w: invalid task transition", ErrInvalidRequest)
	}
	resultJSON := "{}"
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	var current Status
	var requestCiphertext []byte
	if err := tx.QueryRow("SELECT status, request_ciphertext FROM tasks WHERE task_id = ?", taskID).Scan(&current, &requestCiphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return Task{}, err
	}
	if current == next {
		_ = tx.Rollback()
		return s.Get(taskID)
	}
	if current.Terminal() || !allowedTransition(current, next) {
		return Task{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, next)
	}
	request, err := s.decodeRequest(taskID, requestCiphertext)
	if err != nil {
		return Task{}, err
	}
	message = redactSecrets(message, request.Secrets)
	if result != nil {
		cleaned := redactValue(result, request.Secrets)
		encoded, err := json.Marshal(cleaned)
		if err != nil || len(encoded) > 1<<20 {
			return Task{}, fmt.Errorf("%w: invalid task result", ErrInvalidRequest)
		}
		resultJSON = string(encoded)
	}
	now := s.now().UTC().UnixNano()
	var finished any
	if next.Terminal() {
		if err := s.flushPendingLog(tx, taskID); err != nil {
			return Task{}, err
		}
		finished = now
	}
	var exit any
	if exitCode != nil {
		exit = *exitCode
	}
	if _, err := tx.Exec(`UPDATE tasks SET status = ?, message = ?, exit_code = ?,
		updated_at_ns = ?, finished_at_ns = ?, result_json = ? WHERE task_id = ?`,
		next, message, exit, now, finished, resultJSON, taskID); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return s.Get(taskID)
}

func allowedTransition(current, next Status) bool {
	switch current {
	case StatusAccepted:
		return next == StatusRunning || next == StatusCancelling || next == StatusFailed ||
			next == StatusCancelled || next == StatusInterrupted
	case StatusRunning:
		return next == StatusCancelling || next == StatusSucceeded || next == StatusFailed || next == StatusInterrupted
	case StatusCancelling:
		return next == StatusSucceeded || next == StatusFailed || next == StatusCancelled || next == StatusInterrupted
	default:
		return false
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusAccepted, StatusRunning, StatusCancelling, StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

func (s *Store) AppendLog(taskID, text string) (int64, error) {
	if !uuidPattern.MatchString(taskID) || len(text) > MaxLogAppendBytes {
		return 0, fmt.Errorf("%w: invalid task log chunk", ErrInvalidRequest)
	}
	text = strings.ToValidUTF8(text, "\uFFFD")
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var status Status
	var offset int64
	var pendingCiphertext, requestCiphertext []byte
	if err := tx.QueryRow(`SELECT status, log_offset, pending_log, request_ciphertext
		FROM tasks WHERE task_id = ?`, taskID).Scan(&status, &offset, &pendingCiphertext, &requestCiphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if status.Terminal() {
		return 0, ErrTerminalTask
	}
	request, err := s.decodeRequest(taskID, requestCiphertext)
	if err != nil {
		return 0, err
	}
	var pending []byte
	if len(pendingCiphertext) != 0 {
		pending, err = s.open("task-log-pending:"+taskID, pendingCiphertext)
		if err != nil {
			return 0, errors.New("stored task log buffer cannot be decrypted")
		}
	}
	combined := make([]byte, 0, len(pending)+len(text))
	combined = append(combined, pending...)
	combined = append(combined, text...)
	clean := []byte(redactSecrets(string(combined), request.Secrets))
	hold := maxSecretLength(request.Secrets) - 1
	safeLength := len(clean) - hold
	if safeLength < 0 {
		safeLength = 0
	}
	for safeLength > 0 && safeLength < len(clean) && !utf8.RuneStart(clean[safeLength]) {
		safeLength--
	}
	published := clean[:safeLength]
	tail := clean[safeLength:]
	newPending := []byte{}
	if len(tail) != 0 {
		newPending, err = s.seal("task-log-pending:"+taskID, tail)
		if err != nil {
			return 0, err
		}
	}
	if len(published) != 0 {
		if _, err := tx.Exec("INSERT INTO task_logs(task_id, offset, data) VALUES (?, ?, ?)", taskID, offset, published); err != nil {
			return 0, err
		}
		offset += int64(len(published))
	}
	if _, err := tx.Exec("UPDATE tasks SET pending_log = ?, log_offset = ?, updated_at_ns = ? WHERE task_id = ?",
		newPending, offset, s.now().UTC().UnixNano(), taskID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return offset, nil
}

func (s *Store) ReadLogs(taskID string, offset int64, limit int) (LogChunk, error) {
	if !uuidPattern.MatchString(taskID) || offset < 0 || limit < 1 || limit > MaxLogChunkBytes {
		return LogChunk{}, fmt.Errorf("%w: invalid task log query", ErrInvalidRequest)
	}
	task, err := s.Get(taskID)
	if err != nil {
		return LogChunk{}, err
	}
	if offset > task.LogOffset {
		return LogChunk{}, ErrInvalidOffset
	}
	rows, err := s.db.Query(`SELECT offset, data FROM task_logs
		WHERE task_id = ? AND offset + length(data) > ? ORDER BY offset`, taskID, offset)
	if err != nil {
		return LogChunk{}, err
	}
	defer rows.Close()
	buffer := make([]byte, 0, limit)
	for rows.Next() {
		var chunkOffset int64
		var data []byte
		if err := rows.Scan(&chunkOffset, &data); err != nil {
			return LogChunk{}, err
		}
		start := int64(0)
		if offset > chunkOffset {
			start = offset - chunkOffset
		}
		if start < 0 || start > int64(len(data)) {
			return LogChunk{}, errors.New("task log offsets are inconsistent")
		}
		part := data[start:]
		remaining := limit - len(buffer)
		if len(part) > remaining {
			part = part[:remaining]
		}
		buffer = append(buffer, part...)
		if len(buffer) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return LogChunk{}, err
	}
	for len(buffer) > 0 && !utf8.Valid(buffer) {
		buffer = buffer[:len(buffer)-1]
	}
	next := offset + int64(len(buffer))
	if len(buffer) == 0 && offset < task.LogOffset {
		return LogChunk{}, errors.New("task log data is incomplete")
	}
	return LogChunk{
		AgentID: s.agentID, TaskID: taskID, Offset: offset, NextOffset: next,
		Text: string(buffer), EOF: task.Status.Terminal() && next >= task.LogOffset,
		Truncated: false,
	}, nil
}

func (s *Store) markInterruptedOnStartup() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT task_id FROM tasks WHERE status IN (?, ?, ?)",
		StatusAccepted, StatusRunning, StatusCancelling)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	for _, id := range ids {
		if err := s.flushPendingLog(tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE tasks SET status = ?, message = ?,
			updated_at_ns = ?, finished_at_ns = ?, exit_code = NULL WHERE task_id = ?`,
			StatusInterrupted, "Agent restarted before task completion; task was not resumed", now, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) flushPendingLog(tx *sql.Tx, taskID string) error {
	var pendingCiphertext, requestCiphertext []byte
	var offset int64
	if err := tx.QueryRow("SELECT pending_log, request_ciphertext, log_offset FROM tasks WHERE task_id = ?", taskID).
		Scan(&pendingCiphertext, &requestCiphertext, &offset); err != nil {
		return err
	}
	if len(pendingCiphertext) == 0 {
		return nil
	}
	pending, err := s.open("task-log-pending:"+taskID, pendingCiphertext)
	if err != nil {
		return errors.New("stored task log buffer cannot be decrypted")
	}
	request, err := s.decodeRequest(taskID, requestCiphertext)
	if err != nil {
		return err
	}
	clean := []byte(redactSecrets(string(pending), request.Secrets))
	if len(clean) != 0 {
		if _, err := tx.Exec("INSERT INTO task_logs(task_id, offset, data) VALUES (?, ?, ?)", taskID, offset, clean); err != nil {
			return err
		}
		offset += int64(len(clean))
	}
	_, err = tx.Exec("UPDATE tasks SET pending_log = X'', log_offset = ? WHERE task_id = ?", offset, taskID)
	return err
}

func (s *Store) decodeRequest(taskID string, ciphertext []byte) (TaskStart, error) {
	payload, err := s.open("task-request:"+taskID, ciphertext)
	if err != nil {
		return TaskStart{}, errors.New("stored task request cannot be decrypted")
	}
	var request TaskStart
	if err := json.Unmarshal(payload, &request); err != nil {
		return TaskStart{}, errors.New("stored task request is invalid")
	}
	return request, nil
}

func (s *Store) seal(aad string, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, []byte(aad))...), nil
}

func (s *Store) open(aad string, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("encrypted value is too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	return gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], []byte(aad))
}

func redactSecrets(text string, secrets map[string]string) string {
	values := make([]string, 0, len(secrets))
	for _, value := range secrets {
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}

func maxSecretLength(secrets map[string]string) int {
	maximum := 1
	for _, value := range secrets {
		if len(value) > maximum {
			maximum = len(value)
		}
	}
	return maximum
}

func redactValue(value any, secrets map[string]string) any {
	switch typed := value.(type) {
	case string:
		return redactSecrets(typed, secrets)
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			clean[key] = redactValue(item, secrets)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, item := range typed {
			clean[i] = redactValue(item, secrets)
		}
		return clean
	default:
		return value
	}
}

func scanTask(scanner interface{ Scan(...any) error }, agentID string) (Task, error) {
	var task Task
	var exitCode sql.NullInt64
	var finished sql.NullInt64
	var createdNS, updatedNS int64
	var resultJSON string
	if err := scanner.Scan(&task.TaskID, &task.Kind, &task.Action, &task.ApplicationID,
		&task.ResourceID, &task.Status, &task.Message, &exitCode, &createdNS,
		&updatedNS, &finished, &task.LogOffset, &resultJSON); err != nil {
		return Task{}, err
	}
	task.AgentID = agentID
	task.createdAtNS = createdNS
	task.CreatedAt = time.Unix(0, createdNS).UTC()
	task.UpdatedAt = time.Unix(0, updatedNS).UTC()
	if exitCode.Valid {
		code := int(exitCode.Int64)
		task.ExitCode = &code
	}
	if finished.Valid {
		finishedAt := time.Unix(0, finished.Int64).UTC()
		task.FinishedAt = &finishedAt
	}
	if resultJSON != "" && resultJSON != "{}" {
		if err := json.Unmarshal([]byte(resultJSON), &task.Result); err != nil {
			return Task{}, errors.New("stored task result is invalid")
		}
	}
	return task, nil
}

func encodeCursor(createdAtNS int64, taskID string) string {
	value := strconv.FormatInt(createdAtNS, 10) + "\n" + taskID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCursor(cursor string) (int64, string, error) {
	if len(cursor) > 256 {
		return 0, "", ErrInvalidRequest
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", err
	}
	parts := strings.SplitN(string(raw), "\n", 2)
	if len(parts) != 2 || !uuidPattern.MatchString(parts[1]) {
		return 0, "", ErrInvalidRequest
	}
	created, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || created <= 0 {
		return 0, "", ErrInvalidRequest
	}
	return created, parts[1], nil
}
