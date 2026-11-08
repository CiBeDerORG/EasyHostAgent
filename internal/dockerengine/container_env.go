package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// rpcAddress 只从容器启动环境读取 EASYTIER_RPC_PORT，不泄露其余环境变量或密钥。
func rpcAddress(environ []string) (string, error) {
	const key = "EASYTIER_RPC_PORT="
	port := ""
	for _, entry := range environ {
		if !strings.HasPrefix(entry, key) {
			continue
		}
		if port != "" {
			return "", errors.New("重复的 EasyTier RPC 端口配置")
		}
		port = strings.TrimPrefix(entry, key)
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("无效的 EasyTier RPC 端口")
		}
	}
	if port == "" {
		return "", errors.New("容器缺少 EasyTier RPC 端口配置")
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

func (c *Client) EasyTierRPCAddress(ctx context.Context, containerID string) (string, error) {
	if !validContainerID(containerID) {
		return "", ErrProtocol
	}
	var version Version
	if err := c.read(ctx, "/version", &version); err != nil {
		return "", err
	}
	api, err := selectAPI(version)
	if err != nil {
		return "", err
	}
	var details struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := c.read(ctx, fmt.Sprintf("/v%s/containers/%s/json", api, url.PathEscape(containerID)), &details); err != nil {
		return "", err
	}
	return rpcAddress(details.Config.Env)
}
