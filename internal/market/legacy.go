package market

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

const legacyDeployMode = "legacy_deploy"

// 只允许合法应用 ID；真正的可管理性由固定 Git 安装包、Compose 路径和本机脚本共同验证。
func legacyDeployApp(id string) bool {
	return len(id) <= 64 && appID.MatchString(id)
}

func (s *Service) legacyWorkdir(app string) string {
	return filepath.Join(filepath.Dir(s.root), app, "deploy")
}

// A matching name alone never proves ownership. Verify every Compose
// container's project, original working directory and exact Compose file.
func (s *Service) validLegacyProject(p dockerengine.Resource, app string) bool {
	if !legacyDeployApp(app) || p.ComposeProject != app || len(p.Containers) == 0 {
		return false
	}
	work := s.legacyWorkdir(app)
	config := filepath.Join(work, "docker-compose.yaml")
	for _, c := range p.Containers {
		if c.Labels["com.docker.compose.project"] != app ||
			c.Labels["com.docker.compose.project.working_dir"] != work ||
			c.Labels["com.docker.compose.project.config_files"] != config ||
			c.Labels["com.docker.compose.service"] == "" {
			return false
		}
	}
	return true
}

// 历史 OpenCode Compose 没有 name 字段，项目名可能是目录名 deploy。
// 仅在唯一容器的目录、服务及配置文件均匹配时关联只读状态，不授予管理权限。
func (s *Service) legacyOpenCodeProject(p dockerengine.Resource) bool {
	if p.ComposeProject != "deploy" || len(p.Containers) != 1 {
		return false
	}
	work := s.legacyWorkdir("opencode")
	c := p.Containers[0]
	return c.Labels["com.docker.compose.project"] == "deploy" &&
		c.Labels["com.docker.compose.service"] == "opencode" &&
		c.Labels["com.docker.compose.project.working_dir"] == work &&
		c.Labels["com.docker.compose.project.config_files"] == filepath.Join(work, "docker-compose.yaml")
}

// Check the current, pre-existing Deploy scripts only in the fixed legacy
// path. Never follow a symlink or execute scripts supplied as request params.
func (s *Service) legacyScript(app, action string) (string, error) {
	if !legacyDeployApp(app) || !slices.Contains([]string{"install", "update", "uninstall"}, action) {
		return "", errors.New("此应用不允许接管原有 Deploy 脚本")
	}
	softwareRoot := filepath.Dir(s.root)
	work := s.legacyWorkdir(app)
	for _, dir := range []string{
		softwareRoot,
		filepath.Dir(work),
		work,
	} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("原有 Deploy 安装目录不存在或含符号链接")
		}
	}
	for _, name := range []string{"docker-compose.yaml", "install.sh", "update.sh", "uninstall.sh"} {
		p := filepath.Join(work, name)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return "", errors.New("原有 Deploy 脚本缺失或可被其他用户修改，请先更新 fnOS 上的 Deploy 项目")
		}
	}
	return filepath.Join(work, action+".sh"), nil
}

// 关联操作仅建立本机记录：必须先验证固定 Git 包及 Docker Compose 的原始身份，不执行任何脚本。
func (s *Service) Adopt(ctx context.Context, controller string, req Request, authorized func() bool) (Record, error) {
	if !req.Ref.valid() || !legacyDeployApp(req.App) ||
		!bundles.ValidDigest(req.Digest) || len(req.Config) != 0 {
		return Record{}, errors.New("仅支持接管已验证的 Deploy Docker 应用")
	}
	if !s.hasGrant(controller, req.Ref) || !authorized() {
		return Record{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" {
		return Record{}, ErrBusy
	}
	old, err := s.read(req.App)
	if err != nil {
		return Record{}, err
	}
	if old.App != "" && old.Source != req.Source || old.Managed {
		return Record{}, ErrConflict
	}
	info, files, err := s.cache.ReadCandidate(req.Digest)
	if err != nil || info.ApplicationID != req.App || info.SourceID != req.Source {
		return Record{}, errors.New("应用包与所选 Git 来源不匹配")
	}
	m, err := loadManifest(info, files)
	if err != nil || !slices.Contains(m.Compatibility.Platforms, s.platform) {
		return Record{}, errors.New("当前来源的应用清单与目标主机不兼容")
	}
	for _, action := range []string{"install", "update", "uninstall"} {
		a := m.Actions[action]
		if a.Strategy != "script" || a.Path != "deploy/"+action+".sh" || len(a.Argv) != 0 {
			return Record{}, errors.New("只有明确使用 Deploy 原生脚本的应用才可以接管")
		}
	}
	if _, err := s.legacyScript(req.App, "update"); err != nil {
		return Record{}, err
	}
	snap, err := s.scan(ctx)
	if err != nil || !snap.Complete {
		return Record{}, errors.New("无法完整核对目标主机 Docker 项目，接管已取消")
	}
	found := false
	for _, p := range snap.Resources {
		if p.ComposeProject == req.App {
			if found || !s.validLegacyProject(p, req.App) {
				return Record{}, ErrConflict
			}
			found = true
		}
	}
	if !found || !authorized() {
		return Record{}, errors.New("原有 Deploy 项目不存在或控制端授权已变化")
	}
	rec := Record{Ref: req.Ref, Digest: req.Digest, Action: "adopt",
		Result: "succeeded", Managed: true, Mode: legacyDeployMode,
		Services: m.Verification.Services}
	if err := s.save(rec); err != nil {
		return Record{}, errors.New("无法保存原有 Deploy 应用的接管记录")
	}
	return rec, nil
}
