package dockerengine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPathForPlatform(t *testing.T) {
	for _, platform := range []string{"linux", "fnos"} {
		path, err := SocketPathForPlatform(platform)
		if err != nil || path != "/var/run/docker.sock" {
			t.Fatalf("%s 平台 Socket 路径错误：%q, %v", platform, path, err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := SocketPathForPlatform("macos")
	want := filepath.Join(home, ".docker", "run", "docker.sock")
	if err != nil || path != want {
		t.Fatalf("macOS 应使用登录用户的 Docker Desktop Socket：%q，期望 %q，错误 %v", path, want, err)
	}
	t.Setenv("DOCKER_HOST", "tcp://untrusted.example:2375")
	path2, err := SocketPathForPlatform("macos")
	if err != nil || path2 != want {
		t.Fatalf("不应接受 DOCKER_HOST 重定向：%q，错误 %v", path2, err)
	}
	for _, platform := range []string{"", "windows", "darwin"} {
		if _, err := SocketPathForPlatform(platform); !errors.Is(err, ErrProtocol) {
			t.Fatalf("非法平台 %q 未拒绝：%v", platform, err)
		}
	}
}

func TestNewForPlatformDoesNotRequireRunningDocker(t *testing.T) {
	client, err := NewForPlatform("macos")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	path, _ := SocketPathForPlatform("macos")
	if client.socket != path {
		t.Fatalf("没有使用 Docker Desktop Socket：%q != %q", client.socket, path)
	}
}
