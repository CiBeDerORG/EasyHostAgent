package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/CiBeDerORG/EasyHostAgent/internal/taskstore"
)

const (
	DefaultMaxParallel = 4
	MaxParallelLimit   = 64
)

var (
	ErrClosed          = errors.New("task scheduler is closed")
	ErrInvalidLockKey  = errors.New("invalid shared resource lock key")
	ErrTaskCancelled   = errors.New("task cancellation requested")
	ErrSchedulerClosed = errors.New("task scheduler is shutting down")
)

// Executor runs one allowlisted task. Implementations must observe ctx at safe
// checkpoints and must not perform automatic rollback when cancellation occurs.
type Executor interface {
	Execute(ctx context.Context, request taskstore.TaskStart, writeLog func(string) error) (map[string]any, *int, error)
}

type Scheduler struct {
	store    *taskstore.Store
	executor Executor
	parallel chan struct{}
	locker   *keyedLocker
	mu       sync.Mutex
	jobs     map[string]*job
	closed   bool
	wg       sync.WaitGroup
}

type job struct {
	cancel     context.CancelCauseFunc
	extraLocks []string
}

type keyedLocker struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
}

type lockEntry struct {
	token chan struct{}
	refs  int
}

func New(store *taskstore.Store, executor Executor, maxParallel int) (*Scheduler, error) {
	if store == nil || executor == nil {
		return nil, errors.New("task scheduler requires a task store and executor")
	}
	if maxParallel <= 0 {
		maxParallel = DefaultMaxParallel
	}
	if maxParallel > MaxParallelLimit {
		return nil, fmt.Errorf("max parallel tasks cannot exceed %d", MaxParallelLimit)
	}
	return &Scheduler{
		store: store, executor: executor, parallel: make(chan struct{}, maxParallel),
		locker: &keyedLocker{entries: make(map[string]*lockEntry)},
		jobs:   make(map[string]*job),
	}, nil
}

// Submit creates a durable task and schedules it once. A repeated task ID never
// starts a second worker, even if the original request's response was lost.
func (s *Scheduler) Submit(request taskstore.TaskStart) (taskstore.Task, bool, error) {
	return s.SubmitWithLocks(request, nil)
}

// SubmitWithLocks adds manifest-resolved shared resource locks to the normal
// application/resource lock. Lock keys are supplied by trusted server code,
// not by a client-controlled shell command.
func (s *Scheduler) SubmitWithLocks(request taskstore.TaskStart, extraLocks []string) (taskstore.Task, bool, error) {
	locks, err := normalizeExtraLocks(extraLocks)
	if err != nil {
		return taskstore.Task{}, false, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return taskstore.Task{}, false, ErrClosed
	}
	task, reused, err := s.store.Create(request)
	if err != nil {
		s.mu.Unlock()
		return taskstore.Task{}, false, err
	}
	if reused {
		s.mu.Unlock()
		return task, true, nil
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	j := &job{cancel: cancel, extraLocks: locks}
	s.jobs[task.TaskID] = j
	s.wg.Add(1)
	s.mu.Unlock()

	go s.run(ctx, task.TaskID, j)
	return task, false, nil
}

// Cancel requests cancellation. It does not undo changes already made by the
// executor. A running task becomes "cancelling" until the executor reaches a
// safe checkpoint and reports its final outcome.
func (s *Scheduler) Cancel(taskID string) (taskstore.Task, error) {
	s.mu.Lock()
	task, err := s.store.Get(taskID)
	if err != nil {
		s.mu.Unlock()
		return taskstore.Task{}, err
	}
	if task.Status.Terminal() {
		s.mu.Unlock()
		return task, nil
	}
	active, ok := s.jobs[taskID]
	if !ok {
		s.mu.Unlock()
		if task.Status == taskstore.StatusAccepted {
			return s.store.Transition(taskID, taskstore.StatusCancelled, "Task cancelled before execution; no rollback was performed.", nil, nil)
		}
		return s.store.Transition(taskID, taskstore.StatusInterrupted, "No active worker is attached; task was not resumed and no rollback was performed.", nil, nil)
	}
	if task.Status != taskstore.StatusCancelling {
		updated, transitionErr := s.store.Transition(taskID, taskstore.StatusCancelling,
			"Cancellation requested; partial changes and logs will be retained. No rollback will be performed.", nil, nil)
		if transitionErr != nil {
			latest, getErr := s.store.Get(taskID)
			s.mu.Unlock()
			if getErr == nil && latest.Status.Terminal() {
				return latest, nil
			}
			return taskstore.Task{}, transitionErr
		}
		task = updated
	}
	active.cancel(ErrTaskCancelled)
	s.mu.Unlock()
	return task, nil
}

// Wait blocks until all tasks submitted so far have finished. Callers must not
// submit concurrently with Wait.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}

// Shutdown stops accepting new work, requests interruption of active tasks, and
// waits for workers up to ctx's deadline. It never rolls back task side effects.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, active := range s.jobs {
		active.cancel(ErrSchedulerClosed)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) run(ctx context.Context, taskID string, active *job) {
	defer s.wg.Done()
	defer func() {
		active.cancel(nil)
		s.mu.Lock()
		if s.jobs[taskID] == active {
			delete(s.jobs, taskID)
		}
		s.mu.Unlock()
	}()

	request, err := s.store.GetRequest(taskID)
	if err != nil {
		s.finishFailure(taskID, "Stored task request could not be read.", nil, nil)
		return
	}
	releaseLocks, err := s.acquireLocks(ctx, resourceLockKeys(request, active.extraLocks))
	if err != nil {
		s.finishBeforeExecution(taskID, err)
		return
	}
	defer releaseLocks()

	select {
	case s.parallel <- struct{}{}:
		defer func() { <-s.parallel }()
	case <-ctx.Done():
		s.finishBeforeExecution(taskID, context.Cause(ctx))
		return
	}
	if ctx.Err() != nil {
		s.finishBeforeExecution(taskID, context.Cause(ctx))
		return
	}
	current, err := s.store.Get(taskID)
	if err != nil {
		return
	}
	if current.Status.Terminal() {
		return
	}
	if current.Status == taskstore.StatusCancelling {
		s.finishBeforeExecution(taskID, ErrTaskCancelled)
		return
	}
	if _, err := s.store.Transition(taskID, taskstore.StatusRunning, "Task started.", nil, nil); err != nil {
		current, getErr := s.store.Get(taskID)
		if getErr == nil && current.Status == taskstore.StatusCancelling {
			s.finishBeforeExecution(taskID, ErrTaskCancelled)
			return
		}
		s.finishFailure(taskID, "Task could not enter the running state.", nil, nil)
		return
	}

	result, exitCode, executeErr := s.executor.Execute(ctx, request, func(text string) error {
		_, err := s.store.AppendLog(taskID, text)
		return err
	})
	if executeErr == nil {
		_, _ = s.store.Transition(taskID, taskstore.StatusSucceeded, "Task completed successfully.", exitCode, result)
		return
	}
	switch {
	case errors.Is(context.Cause(ctx), ErrTaskCancelled):
		_, _ = s.store.Transition(taskID, taskstore.StatusCancelled,
			"Task cancelled at a safe checkpoint; partial changes and logs were retained. No rollback was attempted.", exitCode, result)
	case errors.Is(context.Cause(ctx), ErrSchedulerClosed):
		_, _ = s.store.Transition(taskID, taskstore.StatusInterrupted,
			"Agent stopped before task completion; task was not resumed and no rollback was attempted.", exitCode, result)
	default:
		_, _ = s.store.Transition(taskID, taskstore.StatusFailed, fmt.Sprintf("Task failed: %v", executeErr), exitCode, result)
	}
}

func (s *Scheduler) finishBeforeExecution(taskID string, cause error) {
	task, err := s.store.Get(taskID)
	if err != nil || task.Status.Terminal() {
		return
	}
	switch {
	case errors.Is(cause, ErrSchedulerClosed):
		_, _ = s.store.Transition(taskID, taskstore.StatusInterrupted,
			"Agent stopped before task completion; task was not resumed and no rollback was attempted.", nil, nil)
	case errors.Is(cause, ErrTaskCancelled), task.Status == taskstore.StatusCancelling:
		_, _ = s.store.Transition(taskID, taskstore.StatusCancelled,
			"Task cancelled before execution reached a safe start point; no rollback was performed.", nil, nil)
	default:
		message := "Task could not start."
		if cause != nil {
			message = fmt.Sprintf("Task could not start: %v", cause)
		}
		_, _ = s.store.Transition(taskID, taskstore.StatusFailed, message, nil, nil)
	}
}

func (s *Scheduler) finishFailure(taskID, message string, exitCode *int, result map[string]any) {
	task, err := s.store.Get(taskID)
	if err != nil || task.Status.Terminal() {
		return
	}
	if task.Status == taskstore.StatusCancelling {
		s.finishBeforeExecution(taskID, ErrTaskCancelled)
		return
	}
	_, _ = s.store.Transition(taskID, taskstore.StatusFailed, message, exitCode, result)
}

func (s *Scheduler) acquireLocks(ctx context.Context, keys []string) (func(), error) {
	releases := make([]func(), 0, len(keys))
	for _, key := range keys {
		release, err := s.locker.acquire(ctx, key)
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, nil
}

func normalizeExtraLocks(keys []string) ([]string, error) {
	if len(keys) > 128 {
		return nil, ErrInvalidLockKey
	}
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || len(key) > 512 || strings.ContainsRune(key, '\x00') {
			return nil, ErrInvalidLockKey
		}
		normalized = append(normalized, "shared-resource:"+key)
	}
	sort.Strings(normalized)
	return deduplicate(normalized), nil
}

func resourceLockKeys(request taskstore.TaskStart, extraLocks []string) []string {
	keys := append([]string(nil), extraLocks...)
	switch request.Kind {
	case "application":
		keys = append(keys, "application:"+request.ApplicationID)
	case "docker_resource":
		keys = append(keys, "docker-resource:"+request.ResourceID)
	case "host":
		keys = append(keys, "host:exclusive")
	}
	sort.Strings(keys)
	return deduplicate(keys)
}

func deduplicate(keys []string) []string {
	if len(keys) < 2 {
		return keys
	}
	result := keys[:1]
	for _, key := range keys[1:] {
		if key != result[len(result)-1] {
			result = append(result, key)
		}
	}
	return result
}

func (l *keyedLocker) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	entry := l.entries[key]
	if entry == nil {
		entry = &lockEntry{token: make(chan struct{}, 1)}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case entry.token <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-entry.token
				l.mu.Lock()
				entry.refs--
				if entry.refs == 0 {
					delete(l.entries, key)
				}
				l.mu.Unlock()
			})
		}, nil
	case <-ctx.Done():
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
		return nil, context.Cause(ctx)
	}
}
