package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeUpdateFixture(t *testing.T) (string, string, string) {
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
	return root, runner, result
}

func TestNativeUpdateRejectsInvalidAction(t *testing.T) {
	root, runner, _ := nativeUpdateFixture(t)
	for _, action := range []string{"", "uninstall", "restart", "update;rm -rf"} {
		if err := queueNativeUpdate(context.Background(), root, action, runner, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
			t.Fatalf("非法更新操作未拒绝: %s %v", action, err)
		}
	}
}

func TestNativeUpdateRejectsUnsafeScript(t *testing.T) {
	root, runner, _ := nativeUpdateFixture(t)
	script := filepath.Join(root, "easyhostagent", "deploy", "update.sh")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/untrusted.sh", script); err != nil {
		t.Fatal(err)
	}
	if err := queueNativeUpdate(context.Background(), root, "update", runner, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
		t.Fatalf("脚本符号链接必须拒绝: %v", err)
	}
	if err := validateNativePath(filepath.Join(root, "deploy", ".env"), false, uint32(os.Geteuid())+1); !errors.Is(err, ErrNativeUpdateUnavailable) {
		t.Fatal("运行配置文件所有者不可信也必须拒绝")
	}
}
