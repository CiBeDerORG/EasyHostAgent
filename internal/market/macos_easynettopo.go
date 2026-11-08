package market

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

// macOSEasyNetTopoMode 只用于已存在的 macOS Deploy 安装；不提供额外安装器或数据库。
const macOSEasyNetTopoMode = "macos_deploy"

// Docker Desktop 使用两个 Compose 文件。只接受固定项目、服务和部署位置，不能仅凭同名容器接管。
func (s *Service) validMacOSEasyNetTopoProject(p dockerengine.Resource) bool {
	if s.platform != "macos" || p.ComposeProject != "easynettopo" || len(p.Containers) != 1 {
		return false
	}
	work := s.legacyWorkdir("easynettopo")
	base := filepath.Join(work, "docker-compose.yaml")
	overrides := base + "," + filepath.Join(work, "docker-compose.macos.yaml")
	labels := p.Containers[0].Labels
	return labels["com.docker.compose.project"] == "easynettopo" &&
		labels["com.docker.compose.service"] == "easynettopo" &&
		labels["com.docker.compose.project.working_dir"] == work &&
		labels["com.docker.compose.project.config_files"] == overrides
}

// 只使用本机已有的 macOS Deploy 入口。配置、脚本、数据库及上层目录必须归当前登录用户所有，
// 禁止符号链接和组/其他用户写入；密钥保持 macOS 原有 0600 权限，不在 Agent 中读取密钥值。
func (s *Service) macOSEasyNetTopoScript() (string, error) {
	if s.platform != "macos" || os.Geteuid() == 0 {
		return "", errors.New("仅支持 macOS 普通用户的原生 Deploy 更新")
	}
	root := filepath.Dir(s.root)
	deploy := filepath.Join(root, "deploy")
	app := filepath.Join(root, "easynettopo")
	for _, path := range []string{root, deploy, filepath.Join(deploy, "macos"), app,
		filepath.Join(app, "deploy"), filepath.Join(app, "data")} {
		st, err := os.Lstat(path)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 ||
			st.Mode().Perm()&0022 != 0 || !macOSDeployOwned(st) {
			return "", errors.New("macOS Deploy 目录不安全或不存在")
		}
	}
	for _, path := range []string{
		filepath.Join(deploy, "deploy.sh"),
		filepath.Join(deploy, "macos", "deploy.sh"),
		filepath.Join(deploy, "macos", "compose-install.sh"),
		filepath.Join(app, "deploy", "docker-compose.yaml"),
		filepath.Join(app, "deploy", "docker-compose.macos.yaml"),
		filepath.Join(app, "data", "app.db"),
		filepath.Join(deploy, ".env"),
		filepath.Join(deploy, ".secrets"),
	} {
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 ||
			st.Mode().Perm()&0022 != 0 || !macOSDeployOwned(st) {
			return "", errors.New("macOS Deploy 文件缺失、可替换或所有权不正确")
		}
		if (filepath.Base(path) == ".env" || filepath.Base(path) == ".secrets") &&
			st.Mode().Perm() != 0600 {
			return "", errors.New("macOS Deploy 配置权限不是 0600")
		}
	}
	return filepath.Join(deploy, "deploy.sh"), nil
}

func macOSDeployOwned(st os.FileInfo) bool {
	owner, ok := st.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Geteuid())
}

// 此入口不接受 install/uninstall/force_update，也不执行 Git 包中的 Linux 脚本。
// 来源与包均由 Start 先行验证，实际执行交给 /opt/software/deploy 的现有 macOS 更新脚本。
func (s *Service) startMacOSEasyNetTopoUpdate(ctx context.Context, req Request, old Record,
	m manifest, authorized func() bool) (Record, error) {
	if m.Version != 2 || !deployLayout(m) ||
		!slices.Contains(m.Compatibility.Platforms, "macos") ||
		!slices.Contains(m.Compatibility.Architectures, runtime.GOARCH) {
		return Record{}, errors.New("当前 EasyNetTopo 清单未声明 macOS 更新支持")
	}
	if old.Managed && old.Mode != macOSEasyNetTopoMode {
		return Record{}, ErrConflict
	}
	if _, err := s.macOSEasyNetTopoScript(); err != nil {
		return Record{}, err
	}
	snap, err := s.scan(ctx)
	if err != nil || !snap.Complete {
		return Record{}, errors.New("无法完整确认 macOS Docker Compose 项目归属")
	}
	found := false
	for _, project := range snap.Resources {
		if project.ComposeProject != "easynettopo" {
			continue
		}
		if found || !s.validMacOSEasyNetTopoProject(project) ||
			!slices.Contains([]string{"running", "stopped", "partial"}, project.Status) {
			return Record{}, ErrConflict
		}
		found = true
	}
	if !found || !authorized() {
		return Record{}, errors.New("macOS EasyNetTopo 部署不存在或配对授权已变化")
	}
	rec := Record{Ref: req.Ref, Mode: macOSEasyNetTopoMode, Digest: req.Digest,
		Action: "update", Result: "running", Managed: true, Services: m.Verification.Services}
	if err := s.save(rec); err != nil {
		return Record{}, errors.New("无法保存 macOS EasyNetTopo 更新记录")
	}
	s.active = req.App
	s.jobs.Add(1)
	go s.run(rec, m, filepath.Dir(s.root), filepath.Join(filepath.Dir(s.root), "deploy"), nil, authorized)
	return rec, nil
}
