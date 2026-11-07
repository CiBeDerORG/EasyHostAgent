package dockertasks

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

const testID = "11111111-1111-4111-8111-111111111111"

func TestDockerTaskPersistsAndIdempotency(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var actions atomic.Int32
	var stateMu sync.Mutex
	state := "running"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			stateMu.Lock()
			current := state
			stateMu.Unlock()
			_, _ = w.Write([]byte(`[{"Id":"web","State":"` + current + `","Labels":{}}]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/web/stop":
			actions.Add(1)
			stateMu.Lock()
			state = "exited"
			stateMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		_ = listener.Close()
	}()
	engine, err := dockerengine.New(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	dir := t.TempDir()
	svc, err := New(dir, "agent", engine)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := Request{TaskID: testID, Kind: "docker_resource", Action: "stop", ResourceID: "container:web"}
	first, err := svc.Submit(req)
	if err != nil || first.Status != "accepted" {
		t.Fatalf("任务应先持久化 accepted 状态：%+v %v", first, err)
	}
	var got Task
	timeout := time.After(4 * time.Second)
	for {
		got, err = svc.Get(testID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "succeeded" || got.Status == "failed" {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Docker 后台任务超时")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got.Status != "succeeded" || got.Result == nil || !got.Result.Verified || actions.Load() != 1 {
		t.Fatalf("容器停止结果错误：%+v, calls=%d", got, actions.Load())
	}
	retry, err := svc.Submit(req)
	if err != nil || retry.TaskID != testID || actions.Load() != 1 {
		t.Fatalf("同请求重复提交不得再次执行：%+v %v", retry, err)
	}
	if _, err := svc.Submit(Request{TaskID: testID, Kind: "docker_resource", Action: "restart", ResourceID: "container:web"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同请求不得复用 Task ID：%v", err)
	}
	if _, err := svc.Submit(Request{TaskID: "not-uuid", Kind: "docker_resource", Action: "stop", ResourceID: "container:web"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("无效 Task ID 通过验证：%v", err)
	}
	onDisk, err := os.Stat(filepath.Join(dir, "docker-tasks", testID+".json"))
	if err != nil || onDisk.Mode().Perm() != 0600 {
		t.Fatalf("任务记录未安全持久化：%v %v", onDisk, err)
	}
	reloaded, err := New(dir, "agent", engine)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reloaded.Get(testID)
	if err != nil || persisted.Status != "succeeded" {
		t.Fatalf("重启后任务状态丢失：%+v %v", persisted, err)
	}
}

func TestDockerTaskInterruptedOnRestart(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "missing.sock")
	engine, err := dockerengine.New(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	dir := t.TempDir()
	svc, err := New(dir, "agent", engine)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pending := Task{AgentID: "agent", TaskID: testID, Kind: "docker_resource",
		Action: "stop", ResourceID: "compose:test", Status: "running", CreatedAt: now, UpdatedAt: now}
	if err := svc.persist(pending); err != nil {
		t.Fatal(err)
	}
	next, err := New(dir, "agent", engine)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := next.Get(testID)
	if err != nil || recovered.Status != "interrupted" {
		t.Fatalf("中断操作不应自动重试：%+v %v", recovered, err)
	}
	if len(next.List(50)) != 1 {
		t.Fatal("最近任务列表缺失")
	}
	if _, err := next.Get("22222222-2222-4222-8222-222222222222"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知任务应返回 NotFound：%v", err)
	}
}
