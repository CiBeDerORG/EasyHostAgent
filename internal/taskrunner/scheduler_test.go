package taskrunner

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/taskstore"
)

const testAgentID = "123e4567-e89b-12d3-a456-426614174000"

type fakeExecutor struct {
	started chan string
	release chan struct{}
}

func (f *fakeExecutor) Execute(ctx context.Context, request taskstore.TaskStart, writeLog func(string) error) (map[string]any, *int, error) {
	name := request.ApplicationID
	if name == "" {
		name = request.ResourceID
	}
	select {
	case f.started <- name:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if err := writeLog("started " + name + "\n"); err != nil {
		return nil, nil, err
	}
	select {
	case <-f.release:
		return map[string]any{"message": "completed"}, intPointer(0), nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func newTestScheduler(t *testing.T, executor Executor, maxParallel int) (*Scheduler, *taskstore.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.New(dir, testAgentID)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := New(store, executor, maxParallel)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = scheduler.Shutdown(ctx)
		_ = store.Close()
	})
	return scheduler, store
}

func taskRequest(id, applicationID string) taskstore.TaskStart {
	return taskstore.TaskStart{TaskID: id, Kind: "application", Action: "install", ApplicationID: applicationID}
}

func waitStarted(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case value := <-started:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task execution")
		return ""
	}
}

func assertNotStarted(t *testing.T, started <-chan string) {
	t.Helper()
	select {
	case value := <-started:
		t.Fatalf("task started before its lock was released: %s", value)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestDifferentApplicationsExecuteInParallel(t *testing.T) {
	executor := &fakeExecutor{started: make(chan string, 2), release: make(chan struct{}, 2)}
	scheduler, store := newTestScheduler(t, executor, 2)
	firstID := "223e4567-e89b-12d3-a456-426614174000"
	secondID := "323e4567-e89b-12d3-a456-426614174000"
	if _, _, err := scheduler.Submit(taskRequest(firstID, "portainer")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scheduler.Submit(taskRequest(secondID, "sub2api")); err != nil {
		t.Fatal(err)
	}
	first := waitStarted(t, executor.started)
	second := waitStarted(t, executor.started)
	if first == second {
		t.Fatalf("expected different applications to execute in parallel, got %q twice", first)
	}
	executor.release <- struct{}{}
	executor.release <- struct{}{}
	scheduler.Wait()
	for _, id := range []string{firstID, secondID} {
		task, err := store.Get(id)
		if err != nil || task.Status != taskstore.StatusSucceeded {
			t.Fatalf("task %s did not succeed: task=%+v err=%v", id, task, err)
		}
	}
}

func TestSameApplicationIsMutuallyExclusive(t *testing.T) {
	executor := &fakeExecutor{started: make(chan string, 2), release: make(chan struct{}, 2)}
	scheduler, store := newTestScheduler(t, executor, 2)
	firstID := "223e4567-e89b-12d3-a456-426614174000"
	secondID := "323e4567-e89b-12d3-a456-426614174000"
	if _, _, err := scheduler.Submit(taskRequest(firstID, "portainer")); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, executor.started)
	if _, _, err := scheduler.Submit(taskRequest(secondID, "portainer")); err != nil {
		t.Fatal(err)
	}
	assertNotStarted(t, executor.started)
	executor.release <- struct{}{}
	waitStarted(t, executor.started)
	executor.release <- struct{}{}
	scheduler.Wait()
	for _, id := range []string{firstID, secondID} {
		task, err := store.Get(id)
		if err != nil || task.Status != taskstore.StatusSucceeded {
			t.Fatalf("task %s did not succeed: task=%+v err=%v", id, task, err)
		}
	}
}

func TestSharedResourceLockSerializesDifferentApplications(t *testing.T) {
	executor := &fakeExecutor{started: make(chan string, 2), release: make(chan struct{}, 2)}
	scheduler, store := newTestScheduler(t, executor, 2)
	firstID := "223e4567-e89b-12d3-a456-426614174000"
	secondID := "323e4567-e89b-12d3-a456-426614174000"
	shared := []string{"/software/shared.env"}
	if _, _, err := scheduler.SubmitWithLocks(taskRequest(firstID, "portainer"), shared); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, executor.started)
	if _, _, err := scheduler.SubmitWithLocks(taskRequest(secondID, "sub2api"), shared); err != nil {
		t.Fatal(err)
	}
	assertNotStarted(t, executor.started)
	executor.release <- struct{}{}
	waitStarted(t, executor.started)
	executor.release <- struct{}{}
	scheduler.Wait()
	for _, id := range []string{firstID, secondID} {
		task, err := store.Get(id)
		if err != nil || task.Status != taskstore.StatusSucceeded {
			t.Fatalf("task %s did not succeed: task=%+v err=%v", id, task, err)
		}
	}
}

func TestCancelStopsAtExecutorCheckpointAndRetainsLogs(t *testing.T) {
	executor := &fakeExecutor{started: make(chan string, 1)}
	scheduler, store := newTestScheduler(t, executor, 1)
	taskID := "223e4567-e89b-12d3-a456-426614174000"
	if _, _, err := scheduler.Submit(taskRequest(taskID, "portainer")); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, executor.started)
	requested, err := scheduler.Cancel(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if requested.Status != taskstore.StatusCancelling {
		t.Fatalf("expected cancellation to be requested, got %+v", requested)
	}
	scheduler.Wait()
	final, err := store.Get(taskID)
	if err != nil || final.Status != taskstore.StatusCancelled {
		t.Fatalf("task was not cancelled: task=%+v err=%v", final, err)
	}
	logs, err := store.ReadLogs(taskID, 0, taskstore.MaxLogChunkBytes)
	if err != nil || logs.Text != "started portainer\n" || !logs.EOF {
		t.Fatalf("cancellation did not preserve logs: logs=%+v err=%v", logs, err)
	}
}

func intPointer(value int) *int { return &value }
