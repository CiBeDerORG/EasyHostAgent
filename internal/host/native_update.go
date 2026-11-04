package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrNativeUpdateUnavailable = errors.New("原生 Agent 更新环境不满足要求")

// QueueSelfUpdate 只接受已配对控制端的固定动作；独立 systemd timer 在 HTTP 返回之后执行。
// 不接收远端 Shell、路径、下载 URL 或任何可执行参数。
func (s *Service) QueueSelfUpdate(ctx context.Context, action string) error {
	if os.Geteuid() != 0 {
		return ErrNativeUpdateUnavailable
	}
	root, err := s.Root()
	if err != nil {
		return ErrNativeUpdateUnavailable
	}
	return queueNativeUpdate(ctx, root, s.platform, action, "systemd-run", "/etc/easyhostagent/managed-by-installer", 0)
}

// fnOS places managed software under the first administrator's home.
// That administrator owns UID 1000 while the Agent runs as root. Only
// /volN/1000 and its exact software subtree qualify; arbitrary user-owned
// directories never become executable update sources.
func fnosNativeUpdateOwner(path, softwareRoot, platform string, uid uint32) bool {
	if platform != "fnos" || uid != 1000 {
		return false
	}
	softwareRoot = filepath.Clean(softwareRoot)
	path = filepath.Clean(path)
	parts := strings.Split(strings.Trim(softwareRoot, "/"), "/")
	if len(parts) != 3 || parts[1] != "1000" || parts[2] != "software" ||
		!strings.HasPrefix(parts[0], "vol") {
		return false
	}
	if _, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "vol"), 10, 32); err != nil {
		return false
	}
	home := filepath.Dir(softwareRoot)
	return path == home || path == softwareRoot ||
		strings.HasPrefix(path, softwareRoot+string(filepath.Separator))
}

func trustedNativeUpdateOwner(path, softwareRoot, platform string, actual, expected uint32) bool {
	return actual == 0 || actual == expected ||
		(expected == 0 && fnosNativeUpdateOwner(path, softwareRoot, platform, actual))
}

func inspectNativeUpdatePath(path string, directory bool, root, platform string, owner uint32, allowSticky bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: 更新依赖路径无法读取：%s: %v", ErrNativeUpdateUnavailable, path, err)
	}
	if st.Mode()&os.ModeSymlink != 0 ||
		(directory && !st.IsDir()) || (!directory && !st.Mode().IsRegular()) {
		return fmt.Errorf("%w: 更新依赖路径不是预期的真实文件或目录：%s", ErrNativeUpdateUnavailable, path)
	}
	if st.Mode().Perm()&0022 != 0 && !(allowSticky && st.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("%w: 更新依赖路径允许组或其他用户修改：%s (mode=%04o)",
			ErrNativeUpdateUnavailable, path, st.Mode().Perm())
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !trustedNativeUpdateOwner(path, root, platform, sys.Uid, owner) {
		return fmt.Errorf("%w: 更新依赖路径所有者不可信：%s",
			ErrNativeUpdateUnavailable, path)
	}
	return nil
}

func validateNativePath(path string, directory bool, root, platform string, owner uint32) error {
	return inspectNativeUpdatePath(path, directory, root, platform, owner, false)
}

func queueNativeUpdate(ctx context.Context, root, platform, action, runner, marker string, owner uint32) error {
	if action != "update" && action != "force-update" ||
		!filepath.IsAbs(root) || root == "/" || filepath.Clean(root) != root {
		return ErrNativeUpdateUnavailable
	}
	// 禁止通过符号链接或其他用户可写目录，把高权限更新入口替换为恶意脚本。
	dirs := []string{root, filepath.Join(root, "easyhostagent"),
		filepath.Join(root, "easyhostagent", "deploy"), filepath.Join(root, "easyhostagent", "native"),
		filepath.Join(root, "easyhostagent", "native", "scripts"), filepath.Join(root, "deploy"),
		filepath.Join(root, "deploy", "lib")}
	for _, dir := range dirs {
		if err := validateNativePath(dir, true, root, platform, owner); err != nil {
			return err
		}
	}
	// 防止 SOFTWARE_ROOT 的任一上层目录通过符号链接重定向。
	for path := filepath.Dir(root); path != "/"; path = filepath.Dir(path) {
		if err := inspectNativeUpdatePath(path, true, root, platform, owner, true); err != nil {
			return err
		}
	}
	scripts := []string{
		filepath.Join(root, "easyhostagent", "deploy", "install.sh"),
		filepath.Join(root, "easyhostagent", "deploy", "update.sh"),
		filepath.Join(root, "easyhostagent", "deploy", "config.sh"),
		filepath.Join(root, "easyhostagent", "native", "scripts", "install.sh"),
		filepath.Join(root, "deploy", "lib", "release-sources.sh"),
		filepath.Join(root, "deploy", ".env"),
		marker,
	}
	for _, path := range scripts {
		if err := validateNativePath(path, false, root, platform, owner); err != nil {
			return err
		}
	}
	// 绝不以请求提供的路径运行脚本，普通更新沿用本地包优先策略；
	// 强制更新明确忽略本地包，重新下载配置中的 Release 并校验 SHA-256。
	script := filepath.Join(root, "easyhostagent", "deploy", "update.sh")
	args := []string{"--quiet", "--collect", "--unit=easyhostagent-self-update",
		"--on-active=5s", "--property=Type=oneshot", "--", "/bin/bash", script}
	if action == "force-update" {
		script = filepath.Join(root, "easyhostagent", "deploy", "install.sh")
		args[len(args)-1] = script
		args = append(args, "force-update")
	}
	resolved, err := exec.LookPath(runner)
	if err != nil || !filepath.IsAbs(resolved) {
		return fmt.Errorf("%w: systemd-run 不可执行或不存在", ErrNativeUpdateUnavailable)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, resolved, args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root"}
	// stderr 可能包含敏感运行环境，不回传；详细原因由开发者从 systemd journal 查询。
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: systemd-run 排队失败，可能已有任务正在执行：%v", ErrNativeUpdateUnavailable, err)
	}
	return nil
}
