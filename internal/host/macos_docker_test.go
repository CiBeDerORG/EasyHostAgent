package host

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestMacOSDockerDesktopSocketStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".docker", "run")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			t.Errorf("不应访问其他 Engine 端点：%s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Version":"28.5.0","ApiVersion":"1.48"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	result := docker(context.Background(), "macos")
	if !result.Available || result.EngineVersion != "28.5.0" {
		t.Fatalf("Mac 本地 Docker Desktop 状态错误：%+v", result)
	}
}

func TestMacOSDockerDesktopNotRunningDoesNotClaimAvailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	result := docker(context.Background(), "macos")
	if result.Available || result.Error == "" {
		t.Fatalf("离线 Docker Desktop 不应虚报可用：%+v", result)
	}
}
