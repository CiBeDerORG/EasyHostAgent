package market

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

// restoreLegacyScripts repairs only missing or unsafe regular Deploy scripts
// using the already validated, immutable Git application bundle. Existing
// safe scripts, Compose configurations, .env, .secrets and data are untouched.
func (s *Service) restoreLegacyScripts(app string, info bundles.CandidateInfo, files map[string][]byte) error {
	work := s.legacyWorkdir(app)
	softwareRoot := filepath.Dir(s.root)
	for _, dir := range []string{softwareRoot, filepath.Dir(work), work} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("原 Deploy 目录缺失或不安全，不能强制更新")
		}
	}
	compose := filepath.Join(work, "docker-compose.yaml")
	st, err := os.Lstat(compose)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return errors.New("原 Compose 文件缺失或不安全，不能强制更新")
	}
	for _, name := range []string{"install.sh", "update.sh", "uninstall.sh"} {
		target := filepath.Join(work, name)
		st, err := os.Lstat(target)
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法检查原 Deploy 脚本")
		}
		if err == nil && !st.Mode().IsRegular() {
			return errors.New("原 Deploy 脚本不是普通文件，拒绝覆盖")
		}
		bundlePath := app + "/deploy/" + name
		raw, ok := files[bundlePath]
		if !ok || len(raw) == 0 {
			return fmt.Errorf("受控应用包缺少 %s，无法修复", name)
		}
		approved := false
		for _, item := range info.Files {
			if item.Destination == bundlePath && item.Mode == "0755" {
				approved = true
				break
			}
		}
		if !approved {
			return errors.New("应用清单没有授权该脚本")
		}
		if err == nil {
			backup := filepath.Join(s.records, fmt.Sprintf("%s-force-update-%s-%d.bak", app, name, time.Now().UnixNano()))
			old, readErr := os.ReadFile(target)
			if readErr != nil || writeFile(backup, old, 0600) != nil {
				return errors.New("备份不安全的旧脚本失败，拒绝覆盖")
			}
		}
		if err := writeFile(target, raw, 0755); err != nil {
			return errors.New("修复原 Deploy 脚本失败")
		}
	}
	_, err = s.legacyScript(app, "update")
	return err
}

// ForceUpdate is an explicit, confirmed recovery path only for a matching
// legacy Sub2API/Portainer Compose project. It does not seize a same-name
// container, import arbitrary scripts or bypass source/controller grants.
func (s *Service) ForceUpdate(ctx context.Context, controller string, req Request, authorized func() bool) (Record, error) {
	if !req.ConfirmExternal || !req.Ref.valid() || !legacyDeployApp(req.App) ||
		!bundles.ValidDigest(req.Digest) || len(req.Config) != 0 {
		return Record{}, errors.New("强制更新仅限明确确认的原有 Sub2API/Portainer Deploy 部署")
	}
	if !s.hasGrant(controller, req.Ref) || !authorized() {
		return Record{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" {
		return Record{}, ErrBusy
	}
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return Record{}, errors.New("Agent 正在关闭或请求已取消")
	}
	old, err := s.read(req.App)
	if err != nil {
		return Record{}, err
	}
	if old.Managed || old.App != "" && old.Source != req.Source {
		return Record{}, ErrConflict
	}
	info, files, err := s.cache.ReadCandidate(req.Digest)
	if err != nil || info.ApplicationID != req.App || info.SourceID != req.Source {
		return Record{}, errors.New("应用包校验失败或来源不匹配")
	}
	m, err := loadManifest(info, files)
	if err != nil || !slices.Contains(m.Compatibility.Platforms, s.platform) ||
		!slices.Contains(m.Compatibility.Architectures, runtime.GOARCH) {
		return Record{}, errors.New("当前应用清单与 Agent 环境不兼容")
	}
	for _, action := range []string{"install", "update", "uninstall"} {
		a := m.Actions[action]
		if a.Strategy != "script" || a.Path != "deploy/"+action+".sh" || len(a.Argv) != 0 {
			return Record{}, errors.New("强制更新要求受控的 Deploy 原生脚本")
		}
	}
	snap, err := s.scan(ctx)
	if err != nil || !snap.Complete {
		return Record{}, errors.New("无法完整核对现有 Docker Compose 项目")
	}
	found := false
	for _, project := range snap.Resources {
		if project.ComposeProject != req.App {
			continue
		}
		if found || !s.validLegacyProject(project, req.App) {
			return Record{}, ErrConflict
		}
		found = true
	}
	if !found || !authorized() {
		return Record{}, errors.New("原有 Deploy 项目不存在或控制端授权失效")
	}
	if err := s.restoreLegacyScripts(req.App, info, files); err != nil {
		return Record{}, err
	}
	if !authorized() {
		return Record{}, ErrDenied
	}
	rec := Record{Ref: req.Ref, Mode: legacyDeployMode, Digest: req.Digest,
		Action: "update", Result: "running", Managed: true, Services: m.Verification.Services}
	if err := s.save(rec); err != nil {
		return Record{}, errors.New("强制更新记录保存失败，尚未执行更新脚本")
	}
	s.active = req.App
	s.jobs.Add(1)
	go s.run(rec, m, filepath.Join(filepath.Dir(s.root), req.App),
		s.legacyWorkdir(req.App), nil, authorized)
	return rec, nil
}
