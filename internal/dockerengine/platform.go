package dockerengine

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPathForPlatform 返回当前宿主机的本地 Docker Unix Socket。
// macOS 不依赖可能未启用或重启后丢失的 /var/run/docker.sock 符号链接。
// 不读取 DOCKER_HOST，也不连接远程 TCP Engine。
func SocketPathForPlatform(platform string) (string, error) {
	switch platform {
	case "linux", "fnos":
		return "/var/run/docker.sock", nil
	case "macos":
		home, err := os.UserHomeDir()
		if err != nil || home == "" || !filepath.IsAbs(home) {
			return "", fmt.Errorf("%w: 无法确定 macOS 登录用户目录", ErrProtocol)
		}
		return filepath.Join(home, ".docker", "run", "docker.sock"), nil
	default:
		return "", fmt.Errorf("%w: 不支持的宿主平台", ErrProtocol)
	}
}

// NewForPlatform 只选择本机受支持的 Unix Socket；Engine 暂停不影响 Agent 启动。
func NewForPlatform(platform string) (*Client, error) {
	path, err := SocketPathForPlatform(platform)
	if err != nil {
		return nil, err
	}
	return New(path)
}
