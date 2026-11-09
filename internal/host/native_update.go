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
	stateDir := filepath.Join(root, "deploy", "state")
	if err := os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("%w: 无法建立受控更新状态目录: %v", ErrNativeUpdateUnavailable, err)
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
	if !filepath.IsAbs(runner) {
		return ErrNativeUpdateUnavailable
	}

	// 固定任务名可合并重复触发，避免每次请求都创建一个新的 launchd 任务。
	const label = "org.cibeder.easyhostagent-self-update"
	domain := fmt.Sprintf("gui/%d/%s", owner, label)
	checkCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
	check := exec.CommandContext(checkCtx, runner, "print", domain)
	checkOutput, checkErr := check.CombinedOutput()
	checkCancel()
	if checkErr == nil {
		// 同名任务仍在排队或执行时合并请求；已退出的残留标签先清理再提交。
		state := string(checkOutput)
		if strings.Contains(state, "state = running") ||
			strings.Contains(state, "state = waiting") ||
			strings.Contains(state, "state = spawn scheduled") {
			return nil
		}
		removeCtx, removeCancel := context.WithTimeout(ctx, 2*time.Second)
		remove := exec.CommandContext(removeCtx, runner, "remove", label)
		removeOutput, removeErr := remove.CombinedOutput()
		removeCancel()
		if removeErr != nil {
			return fmt.Errorf("%w: launchd 中存在已退出的旧更新标签且无法清理：%v；输出：%s",
				ErrNativeUpdateUnavailable, removeErr, strings.TrimSpace(string(removeOutput)))
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: 检查现有 launchd 更新任务时上下文已取消: %v", ErrNativeUpdateUnavailable, ctx.Err())
	}

	stdoutPath := filepath.Join(stateDir, "easyhostagent-update.stdout.log")
	stderrPath := filepath.Join(stateDir, "easyhostagent-update.stderr.log")
	lockPath := filepath.Join(stateDir, "easyhostagent.macos-update.lock")
	for _, path := range []string{stdoutPath, stderrPath, filepath.Join(stateDir, "easyhostagent-update.log"), lockPath} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return fmt.Errorf("%w: 无法创建更新日志 %s: %v", ErrNativeUpdateUnavailable, path, err)
		}
		if err := f.Chmod(0600); err != nil {
			_ = f.Close()
			return fmt.Errorf("%w: 无法设置更新状态文件权限 %s: %v", ErrNativeUpdateUnavailable, path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("%w: 无法关闭更新状态文件 %s: %v", ErrNativeUpdateUnavailable, path, err)
		}
		if err := validateNativePath(path, false, root, "macos", owner); err != nil {
			return err
		}
	}

	// 更新任务自行持有同一把锁；脚本内也会识别锁已持有，避免嵌套抢锁。
	// 结束时清理固定 launchd 标签，下一次更新请求才能重新提交。
	wrapper := `set -u
label="$1"
root="$2"
action="$3"
log_file="$root/deploy/state/easyhostagent-update.log"
log_line() {
  printf '[%s] pid=%s %s\n' "$(/bin/date -u '+%Y-%m-%dT%H:%M:%SZ')" "$$" "$*" >> "$log_file"
}
cleanup() {
  /bin/launchctl remove "$label" >/dev/null 2>&1 || true
  log_line "更新任务结束，已请求清理 launchd 标签"
}
trap cleanup EXIT
log_line "更新任务开始 action=$action"
if /usr/bin/lockf -k -t 300 "$root/deploy/state/easyhostagent.macos-update.lock" /usr/bin/env EASYHOSTAGENT_UPDATE_LOCK_HELD=1 /bin/bash "$root/deploy/macos/agent-install.sh" "$action" "$root"; then
  log_line "更新任务完成 exit=0"
else
  status=$?
  log_line "更新任务失败 exit=$status"
fi
exit 0`
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, runner, "submit",
		"-l", label, "-o", stdoutPath, "-e", stderrPath,
		"--", "/bin/bash", "-c", wrapper,
		"easyhostagent-self-update", label, root, action)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		// 并发请求可能在检查后、提交前抢先创建固定标签；确认已存在则视为成功合并。
		raceCtx, raceCancel := context.WithTimeout(ctx, 2*time.Second)
		race := exec.CommandContext(raceCtx, runner, "print", domain)
		_, raceErr := race.CombinedOutput()
		raceCancel()
		if raceErr == nil {
			return nil
		}
		return fmt.Errorf("%w: launchctl 提交原生更新任务失败（label=%s）：%v；输出：%s",
			ErrNativeUpdateUnavailable, label, err, strings.TrimSpace(string(output)))
	}
	return nil
}
