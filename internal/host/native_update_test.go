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
		if err := queueNativeUpdate(context.Background(), root, "linux", action, runner, marker, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
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
	if err := queueNativeUpdate(context.Background(), root, "linux", "update", runner, marker, uint32(os.Geteuid())); !errors.Is(err, ErrNativeUpdateUnavailable) {
		t.Fatalf("脚本符号链接必须拒绝: %v", err)
	}
	if err := validateNativePath(filepath.Join(root, "deploy", ".env"), false, root, "linux", uint32(os.Geteuid())+1); !errors.Is(err, ErrNativeUpdateUnavailable) {
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
		if err := queueNativeUpdate(context.Background(), root, "linux", tc.action, runner, marker, uint32(os.Geteuid())); err != nil {
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

func TestNativeUpdateFnOSOwnerScope(t *testing.T) {
	const root = "/vol3/1000/software"
	for _, tc := range []struct {
		name, path, softwareRoot, platform string
		uid                                uint32
		allowed                            bool
	}{
		{"software", root, root, "fnos", 1000, true},
		{"admin-home", "/vol3/1000", root, "fnos", 1000, true},
		{"script", root + "/easyhostagent/deploy/install.sh", root, "fnos", 1000, true},
		{"volume-parent", "/vol3", root, "fnos", 1000, false},
		{"etc-marker", "/etc/easyhostagent/managed-by-installer", root, "fnos", 1000, false},
		{"users-group-path", "/vol3/1001/software", root, "fnos", 1000, false},
		{"sibling", root + "-other", root, "fnos", 1000, false},
		{"other-platform", root, root, "linux", 1000, false},
		{"other-user-owner", root, root, "fnos", 1001, false},
		{"other-uid-root", "/vol3/1001/software", "/vol3/1001/software", "fnos", 1000, false},
		{"other-root", "/tmp/1000/software", "/tmp/1000/software", "fnos", 1000, false},
		{"wrong-root", root, root + "/deeper", "fnos", 1000, false},
		{"volume-4", "/vol4/1000/software/deploy", "/vol4/1000/software", "fnos", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fnosNativeUpdateOwner(tc.path, tc.softwareRoot, tc.platform, tc.uid)
			if got != tc.allowed {
				t.Fatalf("fnosNativeUpdateOwner(%q, %q, %q, %d)=%t want=%t",
					tc.path, tc.softwareRoot, tc.platform, tc.uid, got, tc.allowed)
			}
		})
	}
}

func TestNativeUpdateRejectsGroupWritableDirectory(t *testing.T) {
	root, runner, _, marker := nativeUpdateFixture(t)
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	err := queueNativeUpdate(context.Background(), root, "linux", "update", runner, marker, uint32(os.Geteuid()))
	if !errors.Is(err, ErrNativeUpdateUnavailable) || !strings.Contains(err.Error(), root) {
		t.Fatalf("group/world writable source must be rejected with path detail: %v", err)
	}
}
