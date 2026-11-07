// Package dockertasks 为 Docker 资源变更提供持久任务、幂等与操作记录。
// 容器操作不在 HTTP 请求处理协程内执行，也不接收任意 shell 命令。
package dockertasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

var (
	ErrInvalid  = errors.New("无效的 Docker 任务请求")
	ErrConflict = errors.New("任务 ID 或 Docker 资源存在冲突")
	ErrFull     = errors.New("Docker 任务记录已达到上限")
	ErrNotFound = errors.New("Docker 任务不存在")
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var resourcePattern = regexp.MustCompile(`^(compose|container):[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

const maxTasks = 500

type Request struct {
	TaskID     string `json:"task_id"`
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	ResourceID string `json:"resource_id"`
}

type Task struct {
	AgentID    string                     `json:"agent_id"`
	TaskID     string                     `json:"task_id"`
	Kind       string                     `json:"kind"`
	Action     string                     `json:"action"`
	ResourceID string                     `json:"resource_id"`
	Status     string                     `json:"status"`
	CreatedAt  string                     `json:"created_at"`
	UpdatedAt  string                     `json:"updated_at"`
	Error      string                     `json:"error,omitempty"`
	Result     *dockerengine.ActionResult `json:"result,omitempty"`
}

type Service struct {
	mu      sync.Mutex
	dir     string
	agentID string
	engine  *dockerengine.Client
	tasks   map[string]Task
	active  map[string]string
	cancels map[string]context.CancelFunc
}

func valid(req Request) bool {
	return idPattern.MatchString(req.TaskID) &&
		req.Kind == "docker_resource" &&
		(req.Action == "start" || req.Action == "stop" || req.Action == "restart") &&
		resourcePattern.MatchString(req.ResourceID)
}

// New 会将上次启动留下的 accepted/running/cancelling 标记为 interrupted，
// 避免重启后自动重复执行可能已经部分成功的 Docker 操作。
func New(dataDir, agentID string, engine *dockerengine.Client) (*Service, error) {
	if engine == nil || dataDir == "" || agentID == "" {
		return nil, ErrInvalid
	}
	dir := filepath.Join(dataDir, "docker-tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("建立 Docker 任务目录: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Docker 任务目录权限不安全")
	}
	s := &Service{dir: dir, agentID: agentID, engine: engine, tasks: make(map[string]Task),
		active: make(map[string]string), cancels: make(map[string]context.CancelFunc)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !idPattern.MatchString(id) || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("Docker 任务目录存在不安全文件")
		}
		if len(s.tasks) >= maxTasks {
			return nil, ErrFull
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 65536 || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("Docker 任务记录权限或大小不合法")
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Task
		if err := json.Unmarshal(content, &record); err != nil || record.AgentID != agentID ||
			record.TaskID != id || !valid(Request{record.TaskID, record.Kind, record.Action, record.ResourceID}) {
			return nil, fmt.Errorf("Docker 任务记录不合法")
		}
		switch record.Status {
		case "accepted", "running", "cancelling":
			record.Status = "interrupted"
			record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			record.Error = "Agent 已重启，任务执行结果无法确认"
			if err := s.persist(record); err != nil {
				return nil, err
			}
		case "succeeded", "failed", "cancelled", "interrupted":
		default:
			return nil, ErrInvalid
		}
		s.tasks[id] = record
	}
	return s, nil
}

func (s *Service) persist(task Task) error {
	if !idPattern.MatchString(task.TaskID) {
		return ErrInvalid
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".docker-task-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.dir, task.TaskID+".json"))
}

func (s *Service) update(id, status, publicError string, result *dockerengine.ActionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.tasks[id]
	next := old
	next.Status = status
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	next.Error = publicError
	next.Result = result
	if err := s.persist(next); err != nil {
		return err
	}
	s.tasks[id] = next
	return nil
}

// Submit 先落盘 accepted，再启动执行。相同 task_id 与相同请求为幂等重试。
func (s *Service) Submit(req Request) (Task, error) {
	if !valid(req) {
		return Task{}, ErrInvalid
	}
	s.mu.Lock()
	if old, found := s.tasks[req.TaskID]; found {
		s.mu.Unlock()
		if old.Kind == req.Kind && old.Action == req.Action && old.ResourceID == req.ResourceID {
			return old, nil
		}
		return Task{}, ErrConflict
	}
	if len(s.tasks) >= maxTasks {
		s.mu.Unlock()
		return Task{}, ErrFull
	}
	if s.active[req.ResourceID] != "" {
		s.mu.Unlock()
		return Task{}, ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	task := Task{AgentID: s.agentID, TaskID: req.TaskID, Kind: req.Kind,
		Action: req.Action, ResourceID: req.ResourceID, Status: "accepted",
		CreatedAt: now, UpdatedAt: now}
	if err := s.persist(task); err != nil {
		s.mu.Unlock()
		return Task{}, err
	}
	s.tasks[req.TaskID] = task
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	s.cancels[req.TaskID] = cancel
	s.active[req.ResourceID] = req.TaskID
	s.mu.Unlock()
	go s.execute(ctx, req)
	return task, nil
}

func (s *Service) execute(ctx context.Context, req Request) {
	defer func() {
		s.mu.Lock()
		if cancel := s.cancels[req.TaskID]; cancel != nil {
			cancel()
		}
		delete(s.cancels, req.TaskID)
		delete(s.active, req.ResourceID)
		s.mu.Unlock()
	}()
	if ctx.Err() != nil {
		_ = s.update(req.TaskID, "cancelled", "任务已取消", nil)
		return
	}
	if err := s.update(req.TaskID, "running", "", nil); err != nil {
		return
	}
	result, err := s.engine.Act(ctx, req.ResourceID, req.Action)
	status, errorText := "succeeded", ""
	if err != nil {
		status = "failed"
		errorText = dockerengine.PublicError(err)
		if errors.Is(ctx.Err(), context.Canceled) {
			status, errorText = "cancelled", "任务已取消，操作可能已部分执行"
		}
	}
	_ = s.update(req.TaskID, status, errorText, &result)
}

func (s *Service) Get(id string) (Task, error) {
	if !idPattern.MatchString(id) {
		return Task{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, exists := s.tasks[id]
	if !exists {
		return Task{}, ErrNotFound
	}
	return task, nil
}

func (s *Service) List(limit int) []Task {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		list = append(list, task)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt > list[j].CreatedAt
	})
	if len(list) > limit {
		list = list[:limit]
	}
	return list
}

func (s *Service) Cancel(id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, found := s.tasks[id]
	if !found {
		return Task{}, ErrNotFound
	}
	if task.Status != "accepted" && task.Status != "running" && task.Status != "cancelling" {
		return task, nil
	}
	task.Status = "cancelling"
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.persist(task); err != nil {
		return Task{}, err
	}
	s.tasks[id] = task
	if stop := s.cancels[id]; stop != nil {
		stop()
	}
	return task, nil
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.cancels {
		cancel()
	}
}
