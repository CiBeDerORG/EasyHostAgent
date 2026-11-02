package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/trust"
)

var ErrInventoryUnavailable = errors.New("无法取得完整可信的 Docker 资源清单")

type DockerInventory interface {
	InspectGrouped(context.Context) (dockerengine.GroupedInventory, error)
}
type resourceGuard struct {
	cache        *bundles.Store
	docker       DockerInventory
	softwareRoot string
}

// NewGuarded 是实际 Agent 任务准备服务的唯一生产接线点。
// 需要真实 Docker 完整清单与安全的受控软件根目录，不具备条件时应拒绝启动。
func NewGuarded(dataDir, agentID string, grants *pairing.Store, approvals *trust.Store,
	cache *bundles.Store, inventory DockerInventory, softwareRoot string) (*Store, error) {
	if cache == nil || inventory == nil || !filepath.IsAbs(softwareRoot) ||
		filepath.Clean(softwareRoot) != softwareRoot || softwareRoot == "/" ||
		softwareRoot == dataDir || strings.HasPrefix(softwareRoot, dataDir+"/") {
		return nil, ErrInvalid
	}
	store, err := New(dataDir, agentID, grants, approvals)
	if err != nil {
		return nil, err
	}
	store.guard = &resourceGuard{cache: cache, docker: inventory, softwareRoot: softwareRoot}
	return store, nil
}

func (g *resourceGuard) claims(digest, app string) (Footprint, error) {
	raw, err := g.cache.ManifestBytes(digest)
	if err != nil {
		return Footprint{}, err
	}
	return ParseFootprint(raw, app)
}

func (g *resourceGuard) validateDocker(claims Footprint, action string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	snapshot, err := g.docker.InspectGrouped(ctx)
	if err != nil || !snapshot.Complete {
		return ErrInventoryUnavailable
	}
	for _, res := range snapshot.Resources {
		if res.Kind != dockerengine.KindCompose && res.Kind != dockerengine.KindContainer {
			return ErrInventoryUnavailable
		}
		if res.Kind == dockerengine.KindCompose && res.ComposeProject == claims.ComposeProject {
			// 现有项目可作为更新/卸载的预检目标；实际所有权仍需执行器复核。
			if action == "install" {
				return ErrConflict
			}
			continue
		}
		for _, container := range res.Containers {
			if container.ID == "" {
				return ErrInventoryUnavailable
			}
			for _, port := range container.Ports {
				if port.PublicPort == 0 {
					continue
				}
				used := PortClaim{HostIP: port.IP, Published: port.PublicPort, Protocol: port.Type}
				for _, requested := range claims.Ports {
					if used.Protocol != "tcp" && used.Protocol != "udp" && used.Published == requested.Published {
						return ErrConflict
					}
					if used.HostIP != "" && strings.Contains(used.HostIP, ":") &&
						used.Published == requested.Published && used.Protocol == requested.Protocol {
						return ErrConflict // IPv6 wildcard/双栈行为不确定时保守拒绝
					}
					if portOverlaps(used, requested) {
						return ErrConflict
					}
				}
			}
			for _, mount := range container.Mounts {
				if mount.Type == "volume" {
					for _, name := range claims.NamedVolumes {
						if mount.Name == name || (mount.Name == "" && filepath.Base(mount.Source) == name) {
							return ErrConflict
						}
					}
				}
				if mount.Type == "bind" && filepath.IsAbs(mount.Source) {
					installed := filepath.Clean(mount.Source)
					for _, dataPath := range claims.DataPaths {
						target := filepath.Join(g.softwareRoot, filepath.FromSlash(dataPath))
						if installed == target || strings.HasPrefix(target, installed+string(filepath.Separator)) ||
							strings.HasPrefix(installed, target+string(filepath.Separator)) {
							return ErrConflict
						}
					}
				}
			}
		}
	}
	return nil
}

// Preflight 重新验证任务仍属于当前控制中心、仍准备中、批准未撤销、包未变且 Docker 清单完整。
// 这是只读证据，不是可跨锁保存的执行令牌；执行器上线时必须另行做原子状态转换。
func (s *Store) Preflight(controllerID, token, id string) (Task, error) {
	if !taskUUID.MatchString(id) || s.guard == nil {
		return Task{}, ErrInvalid
	}
	var result Task
	err := s.locked(func() error {
		device, at, err := s.authorize(controllerID, token)
		if err != nil {
			return err
		}
		all, err := s.load()
		if err != nil {
			return err
		}
		for _, task := range all {
			if task.ID != id {
				continue
			}
			if !sameOwner(task, controllerID, device, at) || task.Status != Prepared || task.ExecutionEnabled {
				return ErrUnauthorized
			}
			approval, err := s.trust.Check(task.BundleSHA256)
			if err != nil {
				return err
			}
			if approval.AgentID != task.AgentID || approval.SourceID != task.SourceID ||
				approval.ApplicationID != task.ApplicationID || approval.GitRevision != task.GitRevision ||
				approval.ManifestSHA256 != task.ManifestSHA256 {
				return ErrCorrupt
			}
			claims, err := s.guard.claims(task.BundleSHA256, task.ApplicationID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(claims, task.Footprint) {
				return ErrCorrupt
			}
			for _, other := range all {
				if other.ID != task.ID && other.Status == Prepared && claimsConflict(claims, other.Footprint) {
					return ErrConflict
				}
			}
			if err := s.guard.validateDocker(claims, task.Action); err != nil {
				return err
			}
			result = task
			return nil
		}
		return ErrNotFound
	})
	return result, err
}
