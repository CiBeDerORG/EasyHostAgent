package taskstore

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	testAgentID = "123e4567-e89b-12d3-a456-426614174000"
	testTaskID  = "223e4567-e89b-12d3-a456-426614174000"
	testTaskID2 = "323e4567-e89b-12d3-a456-426614174000"
)
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir, testAgentID)
	if err != nil {
		t.Fatal(err)
	}
	return store, dir
}

func testRequest(id string) TaskStart {
	return TaskStart{TaskID: id, Kind: "application", Action: "install", ApplicationID: "portainer", Parameters: map[string]string{"PORTAINER_HTTP_PORT": "9000"}, Secrets: map[string]string{"ADMIN_PASSWORD": "my-secret-password"}}
}

func TestCreateIdempotencyAndEncryptedRequestPersistence(t *testing.T) {
	store, dir := newTestStore(t)
	task, reused, err := store.Create(testRequest(testTaskID))
	if err != nil {
		t.Fatal(err)
	}
	if reused || task.Status != StatusAccepted || task.AgentID != testAgentID {
		t.Fatalf("unexpected new task: reused=%v task=%+v", reused, task)
	}
	duplicate, reused, err := store.Create(testRequest(testTaskID))
	if err != nil || !reused || duplicate.TaskID != task.TaskID {
		t.Fatalf("idempotent duplicate failed: reused=%v err=%v task=%+v", reused, err, duplicate)
	}
	conflictRequest := testRequest(testTaskID)
	conflictRequest.Secrets["ADMIN_PASSWORD"] = "different-secret"
	if _, _, err := store.Create(conflictRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("same ID with different request must conflict, got %v", err)
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "my-secret-password") {
		t.Fatal("public task response leaked a secret")
	}
	request, err := store.GetRequest(testTaskID)
	if err != nil || request.Secrets["ADMIN_PASSWORD"] != "my-secret-password" {
		t.Fatalf("encrypted request did not round-trip: request=%+v err=%v", request, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(dir + "/tasks.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), "my-secret-password") {
		t.Fatal("SQLite database contains a plaintext secret")
	}
	reopened, err := New(dir, testAgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Get(testTaskID)
	if err != nil || persisted.Status != StatusInterrupted {
		t.Fatalf("task did not persist and recover safely: task=%+v err=%v", persisted, err)
	}
}

func TestStateMachineAndStartupInterruptionDoNotResumeTasks(t *testing.T) {
	store, dir := newTestStore(t)
	if _, _, err := store.Create(testRequest(testTaskID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(testTaskID, StatusSucceeded, "invalid direct success", nil, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted task cannot skip execution: %v", err)
	}
	if _, err := store.Transition(testTaskID, StatusRunning, "started", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLog(testTaskID, "setup started\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir, testAgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	task, err := reopened.Get(testTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusInterrupted || task.FinishedAt == nil || !strings.Contains(task.Message, "not resumed") {
		t.Fatalf("running task must be interrupted on restart: %+v", task)
	}
	chunk, err := reopened.ReadLogs(testTaskID, 0, MaxLogChunkBytes)
	if err != nil || !strings.Contains(chunk.Text, "setup started") || !chunk.EOF {
		t.Fatalf("interrupted task log was not retained: chunk=%+v err=%v", chunk, err)
	}
	if _, err := reopened.Transition(testTaskID, StatusSucceeded, "late completion", nil, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal task must not be resumed: %v", err)
	}
}

func TestSecretRedactionAcrossLogChunksAndTerminalFlush(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	if _, _, err := store.Create(testRequest(testTaskID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(testTaskID, StatusRunning, "running", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLog(testTaskID, "prefix my-secret-"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLog(testTaskID, "password suffix\n"); err != nil {
		t.Fatal(err)
	}
	task, err := store.Transition(testTaskID, StatusFailed, "failed while using my-secret-password", intPtr(1), map[string]any{
		"message": "my-secret-password was rejected",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task.Message, "my-secret-password") || strings.Contains(task.Result["message"].(string), "my-secret-password") {
		t.Fatal("task message or result leaked a secret")
	}
	chunk, err := store.ReadLogs(testTaskID, 0, MaxLogChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(chunk.Text, "my-secret-password") || !strings.Contains(chunk.Text, "[REDACTED]") {
		t.Fatalf("log redaction failed: %q", chunk.Text)
	}
	if !chunk.EOF || chunk.NextOffset != task.LogOffset {
		t.Fatalf("terminal log cursor mismatch: chunk=%+v task=%+v", chunk, task)
	}
}

func TestListCursorAndUTF8ByteOffsets(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	fixed := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	firstRequest := testRequest(testTaskID)
	firstRequest.Secrets = nil
	secondRequest := testRequest(testTaskID2)
	secondRequest.Secrets = nil
	if _, _, err := store.Create(firstRequest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(secondRequest); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("first page invalid: page=%+v err=%v", page, err)
	}
	next, err := store.List(1, *page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.NextCursor != nil || next.Items[0].TaskID == page.Items[0].TaskID {
		t.Fatalf("second page invalid: page=%+v err=%v", next, err)
	}
	if _, err := store.Transition(testTaskID, StatusRunning, "running", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLog(testTaskID, "甲乙丙"); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadLogs(testTaskID, 0, 4)
	if err != nil || first.Text != "甲" || first.NextOffset != 3 {
		t.Fatalf("UTF-8 byte cursor split a rune: chunk=%+v err=%v", first, err)
	}
	second, err := store.ReadLogs(testTaskID, first.NextOffset, 4)
	if err != nil || second.Text != "乙" || second.NextOffset != 6 {
		t.Fatalf("second UTF-8 chunk invalid: chunk=%+v err=%v", second, err)
	}
}

func intPtr(value int) *int { return &value }
