package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeUpdateFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "software")
	for _, dir := range []string{
		root, filepath.Join(root, "easyhostagent", "deploy"),
		filepath.Join(root, "easyhostagent", "native", "scripts"),
		filepath.Join(root, "deploy", "lib"),
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"easyhostagent/deploy/install.sh",
		"easyhostagent/deploy/update.sh",
		"easyhostagent/deploy/config.sh",
		"easyhostagent/native/scripts/install.sh",
		"deploy/lib/release-sources.sh",
		"deploy/.env",
	} {
		target := filepath.Join(root, name)
		if err := os.WriteFile(target, []byte("test\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 生产路径 marker 由安装器保证，此单元测试不会更改系统文件；
	// 通过内部检查辅助函数验证本地路径权限及命令调度。
	runner := filepath.Join(t.TempDir(), "systemd-run")
	result := filepath.Join(t.TempDir(), "arguments")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + result + "'\n"
	if err := os.WriteFile(runner, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "managed-by-installer")
	if err := os.WriteFile(marker, []byte("managed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, runner, result, marker
}

func TestNativeUpdateRejectsInvalidAction(t *testing.T) {
	root, runner, _, marker := nativeUpdateFixture(t)
	for _, action := range []string{"", "uninstall", "restart", "update;rm -rf"} {
		if err := queueNativeUpdate(context.Background(), root, action, runner, marker, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
			t.Fatalf("非法更新操作未拒绝: %s %v", action, err)
		}
	}
}

func TestNativeUpdateRejectsUnsafeScript(t *testing.T) {
	root, runner, _, marker := nativeUpdateFixture(t)
	script := filepath.Join(root, "easyhostagent", "deploy", "update.sh")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/untrusted.sh", script); err != nil {
		t.Fatal(err)
	}
	if err := queueNativeUpdate(context.Background(), root, "update", runner, marker, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
		t.Fatalf("脚本符号链接必须拒绝: %v", err)
	}
	if err := validateNativePath(filepath.Join(root, "deploy", ".env"), false, uint32(os.Geteuid())+1); !errors.Is(err, ErrNativeUpdateUnavailable) {
		t.Fatal("运行配置文件所有者不可信也必须拒绝")
	}
}

func TestNativeUpdateQueuesDetachedSystemdJob(t *testing.T) {
	root, runner, result, marker := nativeUpdateFixture(t)
	for _, tc := range []struct {
		action string
		script string
	}{
		{"update", "update.sh"},
		{"force-update", "install.sh"},
	} {
		if err := queueNativeUpdate(context.Background(), root, tc.action, runner, marker, uint32(os.Geteuid())); err != nil {
			t.Fatalf("%s 调度失败: %v", tc.action, err)
		}
		raw, err := os.ReadFile(result)
		if err != nil {
			t.Fatal(err)
		}
		out := string(raw)
		for _, must := range []string{"--on-active=5s", "--unit=easyhostagent-self-update",
			"/bin/bash", filepath.Join(root, "easyhostagent", "deploy", tc.script)} {
			if !strings.Contains(out, must) {
				t.Fatalf("%s 没有正确生成独立任务参数: %s", tc.action, out)
			}
		}
		if tc.action == "force-update" && !strings.Contains(out, "force-update") {
			t.Fatalf("强制更新未忽略本地缓存: %s", out)
		}
		if strings.Contains(out, "uninstall") {
			t.Fatal("原生 Agent 不允许远程卸载")
		}
	}
}
