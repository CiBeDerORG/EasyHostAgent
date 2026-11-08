// Package easytier 将官方 EasyTier CLI 的只读 JSON 输出转换为统一 Driver 结果。
package easytier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

type Driver struct{ engine *dockerengine.Client }

func New(engine *dockerengine.Client) *Driver { return &Driver{engine: engine} }
func (d *Driver) Type() string                { return "easytier" }
func (d *Driver) Capabilities() []string      { return []string{"node", "peer", "route"} }

func (d *Driver) Read(ctx context.Context, resourceID, capability string) (json.RawMessage, error) {
	if resourceID != "compose:easytier" {
		return nil, errors.New("EasyTier 仅接受现有 compose:easytier 资源")
	}
	switch capability {
	case "node", "peer", "route":
	default:
		return nil, errors.New("不支持的 EasyTier 查询")
	}
	snapshot, err := d.engine.InspectGrouped(ctx)
	if err != nil || !snapshot.Complete {
		return nil, errors.New("无法完整确认 EasyTier Docker 容器身份")
	}
	containerID := ""
	for _, item := range snapshot.Resources {
		if item.ID != resourceID {
			continue
		}
		if item.ComposeProject != "easytier" || len(item.Containers) != 1 {
			return nil, errors.New("EasyTier Compose 项目不符合预期")
		}
		container := item.Containers[0]
		if container.State != "running" || container.Labels["com.docker.compose.service"] != "easytier" {
			return nil, errors.New("EasyTier 容器未正常运行")
		}
		// 仅针对官方镜像执行固定查询，避免把 Docker Exec 投向任意第三方容器。
		image := strings.ToLower(container.Image)
		if !strings.HasPrefix(image, "ghcr.io/easytier/easytier:") &&
			!strings.HasPrefix(image, "ghcr.io/easytier/easytier@") &&
			!strings.HasPrefix(image, "easytier/easytier:") &&
			!strings.HasPrefix(image, "easytier/easytier@") {
			return nil, errors.New("目标容器不是 EasyTier 官方镜像")
		}
		containerID = container.ID
		break
	}
	if containerID == "" {
		return nil, errors.New("未发现 EasyTier 应用容器")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// 读取容器当前配置的 RPC 端口，支持用户在应用商店自定义端口。
	rpcAddress, err := d.engine.EasyTierRPCAddress(queryCtx, containerID)
	if err != nil {
		return nil, errors.New("无法确认 EasyTier RPC 端口")
	}
	// 不运行 Shell，不接收浏览器传来的任意参数；RPC 仅在容器本地访问。
	output, err := d.engine.Exec(queryCtx, containerID,
		[]string{"easytier-cli", "-p", rpcAddress, "-v", "-o", "json", capability})
	if err != nil {
		return nil, fmt.Errorf("EasyTier CLI 查询失败: %w", err)
	}
	if !json.Valid(output) {
		return nil, errors.New("官方 EasyTier CLI 未返回有效 JSON")
	}
	return json.RawMessage(output), nil
}
