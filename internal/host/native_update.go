package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// QueueSelfUpdate 只接受已配对控制端的固定动作；Linux/fnOS 使用 systemd，macOS 使用用户 launchd 延迟执行。
// 不接收远端 Shell、路径、下载 URL 或任何可执行参数。
func (s *Service) QueueSelfUpdate(ctx context.Context, action string) error {
	root, err := s.Root()
	if err != nil {
		return ErrNativeUpdateUnavailable
	}
	if s.platform == "macos" {
		if os.Geteuid() == 0 {
			return ErrNativeUpdateUnavailable
		}
		return queueMacNativeUpdate(ctx, root, action, "/bin/launchctl", uint32(os.Geteuid()))
	}
	if os.Geteuid() != 0 {
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


// macOS 使用登录用户的 launchd 创建独立一次性更新任务。命令固定且不接受远程路径。
// 更新脚本始终使用仓库统一 Release 和 SHA-256 校验；服务重启不会杀死更新任务。
func queueMacNativeUpdate(ctx context.Context, root, action, runner string, owner uint32) error {
	if (action != "update" && action != "force-update") ||
		!filepath.IsAbs(root) || root == "/" || filepath.Clean(root) != root {
		return ErrNativeUpdateUnavailable
	}
	for _, dir := range []string{
		root, filepath.Join(root, "deploy"), filepath.Join(root, "deploy", "macos"),
		filepath.Join(root, "deploy", "lib"),
		filepath.Join(root, "easyhostagent"), filepath.Join(root, "easyhostagent", "native"),
		filepath.Join(root, "easyhostagent", "native", "bin"),
	} {
		if err := validateNativePath(dir, true, root, "macos", owner); err != nil {
			return err
		}
	}
	// 老版 macOS 安装器未必创建 state；仅在根目录校验后创建固定目录。
	stateDir := filepath.Join(root, "deploy", "state")
	if err := os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("%w: 无法建立受控更新状态目录", ErrNativeUpdateUnavailable)
	}
	if err := validateNativePath(stateDir, true, root, "macos", owner); err != nil {
		return err
	}
	for path := filepath.Dir(root); path != "/"; path = filepath.Dir(path) {
		if err := inspectNativeUpdatePath(path, true, root, "macos", owner, true); err != nil {
			return err
		}
	}
	for _, path := range []string{
		filepath.Join(root, "deploy", "macos", "agent-install.sh"),
		filepath.Join(root, "deploy", "lib", "versions.sh"),
		filepath.Join(root, "deploy", "lib", "versions.env"),
		filepath.Join(root, "deploy", ".env"),
		filepath.Join(root, "easyhostagent", "native", ".managed-by-macos-deploy"),
		filepath.Join(root, "easyhostagent", "native", "bin", "easyhostagent"),
	} {
		if err := validateNativePath(path, false, root, "macos", owner); err != nil {
			return err
		}
	}
	// launchctl submit 自动重试非零退出的任务；包装层即使下载失败也以零退出，
	// 让控制端判定更新未成功，而不是无限重复执行可能有副作用的更新。
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrNativeUpdateUnavailable
	}
	if !filepath.IsAbs(runner) {
		return ErrNativeUpdateUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runner, "submit",
		"-l", "org.cibeder.easyhostagent-self-update."+hex.EncodeToString(nonce[:]),
		"-o", filepath.Join(root, "deploy", "state", "easyhostagent-update.stdout.log"),
		"-e", filepath.Join(root, "deploy", "state", "easyhostagent-update.stderr.log"),
		"--", "/bin/bash", "-c",
		`sleep 5; /usr/bin/lockf -t 0 "$1/deploy/state/easyhostagent.macos-update.lock" /bin/bash "$1/deploy/macos/agent-install.sh" "$2" "$1"; exit 0`,
		"easyhostagent-self-update", root, action)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: launchctl 无法提交原生更新任务: %v", ErrNativeUpdateUnavailable, err)
	}
	return nil
}
