package market

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"regexp"
	"syscall"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

var safeLegacyFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*package market

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"regexp"
	"syscall"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

)

// backupLegacyTarget saves the exact existing regular file contents, or the
// literal target of a symlink, without ever following a symlink.
func (s *Service) backupLegacyTarget(app, name, target string, st os.FileInfo) error {
	var content []byte
	if st.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(target)
		if err != nil {
			return err
		}
		content = []byte("SYMLINK -> " + link + "\n")
	} else if st.Mode().IsRegular() {
		f, err := os.OpenFile(target, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		actual, statErr := f.Stat()
		if statErr != nil || !actual.Mode().IsRegular() {
			_ = f.Close()
			return errors.New("原文件在备份时发生变化")
		}
		content, err = io.ReadAll(io.LimitReader(f, (8<<20)+1))
		closeErr := f.Close()
		if err != nil || closeErr != nil || len(content) > 8<<20 {
			return errors.New("原文件读取失败或超过备份大小限制")
		}
	} else {
		return errors.New("原路径不是普通文件或符号链接，拒绝覆盖")
	}
	backup := filepath.Join(s.records, fmt.Sprintf("%s-force-update-%s-%d.bak", app, name, time.Now().UnixNano()))
	out, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = out.Write(content); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(backup)
		return err
	}
	return closeErr
}

func writeLegacyFile(dir, target string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, ".easyhostagent-force-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename replaces a symlink itself; it never writes through its target.
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// restoreLegacyScripts 从固定 Git 包修复 Deploy 目录中的已声明文件。
// 只处理 deploy/ 下的普通顶层文件，不覆盖安全的现存文件、密钥或数据。
func (s *Service) restoreLegacyScripts(app string, info bundles.CandidateInfo, files map[string][]byte) error {
	work := s.legacyWorkdir(app)
	softwareRoot := filepath.Dir(s.root)
	for _, dir := range []string{softwareRoot, filepath.Dir(work), work} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("原 Deploy 目录缺失或不安全，不能强制更新")
		}
	}
	required := map[string]bool{
		"docker-compose.yaml": false,
		"install.sh": false,
		"update.sh": false,
		"uninstall.sh": false,
	}
	prefix := app + "/deploy/"
	for _, item := range info.Files {
		if !strings.HasPrefix(item.Destination, prefix) {
			continue
		}
		name := strings.TrimPrefix(item.Destination, prefix)
		// 禁止写入嵌套路径、隐藏配置、运行数据和任意文件名。
		if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") ||
			!safeLegacyFileName.MatchString(name) || name == "data" || name == "runtime" {
			continue
		}
		mode := os.FileMode(0644)
		switch item.Mode {
		case "", "0644":
		case "0755":
			mode = 0755
		default:
			return fmt.Errorf("可信应用包的 %s 文件权限声明无效", name)
		}
		raw, ok := files[item.Destination]
		if !ok || int64(len(raw)) != item.SizeBytes {
			return fmt.Errorf("可信应用包缺少完整的 %s 文件", name)
		}
		target := filepath.Join(work, name)
		st, err := os.Lstat(target)
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 {
			if _, exists := required[name]; exists {
				required[name] = true
			}
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查原 Deploy 文件 %s 失败", name)
		}
		if err == nil {
			if !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("原 Deploy 文件 %s 是特殊文件，拒绝覆盖", name)
			}
			if err := s.backupLegacyTarget(app, name, target, st); err != nil {
				return fmt.Errorf("备份原 Deploy 文件 %s 失败", name)
			}
		}
		if err := writeLegacyFile(work, target, raw, mode); err != nil {
			return fmt.Errorf("恢复 Deploy 文件 %s 失败", name)
		}
		if _, exists := required[name]; exists {
			required[name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("固定 Git 应用包缺少 deploy/%s，不能强制更新", name)
		}
	}
	_, err := s.legacyScript(app, "update")
	return err
}

// 强制更新仅处理 Compose 身份与原 Deploy 目录完全匹配的已有应用，拒绝同名外部容器。
// 对已受管项目，强制更新依然走完整的更新校验与已授权脚本，不绕过身份和来源检查。
func (s *Service) ForceUpdate(ctx context.Context, controller string, req Request, authorized func() bool) (Record, error) {
	if !req.ConfirmExternal || !req.Ref.valid() || !bundles.ValidDigest(req.Digest) || len(req.Config) != 0 {
		return Record{}, errors.New("强制更新需要明确确认及有效的固定版本安装包")
	}
	if !s.hasGrant(controller, req.Ref) || !authorized() {
		return Record{}, ErrDenied
	}
	s.mu.Lock()
	old, err := s.read(req.App)
	s.mu.Unlock()
	if err != nil {
		return Record{}, err
	}
	if old.Managed {
		if old.Source != req.Source {
			return Record{}, ErrConflict
		}
		return s.Start(ctx, controller, "update", req, authorized)
	}
	return s.forceUpdateLegacy(ctx, controller, req, authorized)
}

func (s *Service) forceUpdateLegacy(ctx context.Context, controller string, req Request, authorized func() bool) (Record, error) {
	if !req.ConfirmExternal || !req.Ref.valid() || !legacyDeployApp(req.App) ||
		!bundles.ValidDigest(req.Digest) || len(req.Config) != 0 {
		return Record{}, errors.New("强制更新仅限已确认的原有 Deploy 应用")
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
