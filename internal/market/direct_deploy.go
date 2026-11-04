package market

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

const directDeployMode = "deploy_direct"

// 标准 Deploy 清单必须明确使用固定目录与三个受控脚本。
func deployLayout(m manifest) bool {
	if m.Runtime.Kind != "docker_compose" || m.Runtime.File != "deploy/docker-compose.yaml" || m.Runtime.Project != m.ID {
		return false
	}
	for _, name := range []string{"install", "update", "uninstall"} {
		a := m.Actions[name]
		if a.Strategy != "script" || a.Path != "deploy/"+name+".sh" || len(a.Argv) != 0 {
			return false
		}
	}
	return true
}

// 新的标准应用直接落地到 Deploy 约定目录；目录须不可被普通用户篡改。
func trustedManagedDir(dir, softwareRoot, platform string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
		return errors.New("Deploy 目录不存在或权限不安全")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !trustedPathOwner(dir, softwareRoot, platform, owner.Uid) {
		return fmt.Errorf("Deploy 目录所有者不可信：%s", dir)
	}
	return nil
}

// 只写清单中应用自身的文件及 deploy/lib/ 下的共享库；用户配置、数据绝不由安装包覆盖。
func materializeDeploy(softwareRoot, platform string, info bundles.CandidateInfo, files map[string][]byte) error {
	if err := ensureSoftwareRoot(softwareRoot, platform); err != nil {
		return err
	}
	for _, item := range info.Files {
		destination := item.Destination
		if !strings.HasPrefix(destination, info.ApplicationID+"/") &&
			!strings.HasPrefix(destination, "deploy/lib/") {
			return errors.New("安装包包含超出 Deploy 目录的文件")
		}
		parts := strings.Split(destination, "/")
		for _, part := range parts {
			if part == "" || strings.HasPrefix(part, ".") || !safeLegacyFileName.MatchString(part) ||
				part == "data" || part == "runtime" || part == ".env" || part == ".secrets" {
				return errors.New("安装包包含不安全或用户维护的路径")
			}
		}
		mode := os.FileMode(0644)
		switch item.Mode {
		case "", "0644":
		case "0755":
			mode = 0755
		default:
			return errors.New("安装包文件权限无效")
		}
		content, exists := files[destination]
		if !exists || int64(len(content)) != item.SizeBytes {
			return errors.New("安装包文件内容与清单不一致")
		}
		dir := softwareRoot
		for _, part := range parts[:len(parts)-1] {
			dir = filepath.Join(dir, part)
			if err := trustedManagedDir(dir, softwareRoot, platform); err != nil {
				return fmt.Errorf("部署目录不可用: %w", err)
			}
		}
		target := filepath.Join(dir, parts[len(parts)-1])
		st, err := os.Lstat(target)
		if err == nil {
			if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
				return errors.New("已有部署文件类型或权限不安全，禁止覆盖")
			}
			owner, ok := st.Sys().(*syscall.Stat_t)
			if !ok || !trustedPathOwner(target, softwareRoot, platform, owner.Uid) {
				return errors.New("已有部署文件所有者不可信")
			}
			if strings.HasPrefix(destination, "deploy/lib/") ||
				strings.HasPrefix(destination, info.ApplicationID+"/config/") {
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeFile(target, content, mode); err != nil {
			return err
		}
	}
	return nil
}

// startDirectDeploy 只在 Service 持锁且通过控制端配对校验后调用。
func (s *Service) startDirectDeploy(ctx context.Context, req Request, name string, old Record,
	m manifest, info bundles.CandidateInfo, files map[string][]byte, authorized func() bool) (Record, error) {
	if !deployLayout(m) || !slices.Contains(m.Compatibility.Platforms, s.platform) ||
		!slices.Contains(m.Compatibility.Architectures, runtime.GOARCH) {
		return Record{}, errors.New("当前应用清单不符合原生 Deploy 目录契约")
	}
	if old.Managed && old.Mode != directDeployMode || old.Mode != "" && old.Mode != directDeployMode {
		return Record{}, ErrConflict
	}
	snap, err := s.scan(ctx)
	if err != nil || !snap.Complete {
		return Record{}, errors.New("无法完整确认 Docker Compose 项目归属")
	}
	found := false
	for _, project := range snap.Resources {
		if project.ComposeProject != req.App {
			continue
		}
		if found || !old.Managed || !s.validLegacyProject(project, req.App) {
			return Record{}, ErrConflict
		}
		found = true
	}
	if name == "update" && !found || name == "uninstall" && !found {
		return Record{}, errors.New("原部署的 Docker 项目不存在，无法安全更新或卸载")
	}
	if name == "install" && found {
		if old.Managed && old.Digest == req.Digest {
			return old, nil
		}
		return Record{}, ErrConflict
	}
	softwareRoot := filepath.Dir(s.root)
	if name != "uninstall" {
		if err := materializeDeploy(softwareRoot, s.platform, info, files); err != nil {
			return Record{}, fmt.Errorf("无法同步 Deploy 应用文件: %w", err)
		}
	}
	if _, err := s.legacyScript(req.App, name); err != nil {
		return Record{}, err
	}
	var values map[string]string
	if name == "install" {
		values, err = prepareConfig(softwareRoot, s.platform, m.Config, req.Config, true)
		if err != nil {
			return Record{}, err
		}
	}
	if !authorized() {
		return Record{}, ErrDenied
	}
	rec := Record{Ref: req.Ref, Mode: directDeployMode, Digest: req.Digest, Action: name,
		Result: "running", Managed: true, Services: m.Verification.Services}
	if err := s.save(rec); err != nil {
		return Record{}, errors.New("无法保存 Deploy 应用操作记录")
	}
	s.active = req.App
	s.jobs.Add(1)
	go s.run(rec, m, softwareRoot, s.legacyWorkdir(req.App), values, authorized)
	return rec, nil
}
