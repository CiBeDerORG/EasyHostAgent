package market

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

func maestroFixture(t *testing.T) (*Service, Request) {
	t.Helper()
	s, err := New(context.Background(), config.Config{DataDir: t.TempDir(), SoftwareRoot: t.TempDir(), Platform: "macos"},
		"", nil, func(context.Context) (dockerengine.GroupedInventory, error) {
			t.Fatal("原生 Maestro 不应调用 Docker")
			return dockerengine.GroupedInventory{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	s.brewPrefix = t.TempDir()
	bin := filepath.Join(s.brewPrefix, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// 模拟 Homebrew 的安装收据与链接，记录固定参数并允许模拟依赖安装失败。
	const brew = `#!/bin/bash
set -eu
p="$(dirname "$(dirname "$0")")"
echo "$*" >> "$p/calls"
case "$*" in
'list --formula --full-name')
    if [[ -e "$p/opt/maestro" ]]; then echo mobile-dev-inc/tap/maestro; fi ;;
'install --formula mobile-dev-inc/tap/maestro')
    keg="$p/Cellar/maestro/2.0.0"
    mkdir -p "$keg/bin" "$p/opt/openjdk/libexec/openjdk.jdk/Contents/Home/bin"
    echo '{"source":{"tap":"mobile-dev-inc/tap"}}' > "$keg/INSTALL_RECEIPT.json"
    printf '#!/bin/bash\n[[ "$1" == "--version" && -x "$JAVA_HOME/bin/java" ]]\n' > "$keg/bin/maestro"
    chmod 0755 "$keg/bin/maestro"
    ln -s "$keg" "$p/opt/maestro"
    ln -s "$keg/bin/maestro" "$p/bin/maestro"
    cp /usr/bin/true "$p/opt/openjdk/libexec/openjdk.jdk/Contents/Home/bin/java" ;;
'install --cask android-platform-tools')
    if [[ -e "$p/fail-adb" ]]; then echo '安装 ADB 失败' >&2; exit 42; fi
    cp /usr/bin/true "$p/bin/adb" ;;
'--prefix openjdk') echo "$p/opt/openjdk" ;;
*) exit 8 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(brew), 0755); err != nil {
		t.Fatal(err)
	}
	return s, Request{Ref: Ref{Source: testSource, App: "maestro"}, NativeInstaller: "maestro"}
}

func TestMacOSMaestroInstallAndRetry(t *testing.T) {
	s, req := maestroFixture(t)
	if !s.supportsMaestro() {
		t.Skip("安装入口需要 macOS 普通用户")
	}
	status := func() Status {
		t.Helper()
		rows, err := s.Status(context.Background(), testController, []Ref{req.Ref}, true)
		if err != nil || len(rows) != 1 {
			t.Fatalf("读取 Maestro 状态失败：%+v %v", rows, err)
		}
		return rows[0]
	}
	if initial := status(); initial.State != "not_installed" || !initial.Allowed || initial.Mode != macOSHomebrewMode {
		t.Fatalf("未安装状态不正确：%+v", initial)
	}
	run := func() {
		t.Helper()
		rec, err := s.Start(context.Background(), testController, "install", req, func() bool { return true })
		if err != nil || rec.Result != "running" {
			t.Fatalf("安装未受理：%+v，%v", rec, err)
		}
		s.Wait()
	}
	// 用 fixture 自己的 adb 避免依赖开发者机器上的 Android SDK 状态。
	adb := filepath.Join(s.brewPrefix, "bin", "adb")
	if err := os.WriteFile(adb, []byte("#!/bin/sh\necho 'ADB 验证失败' >&2\nexit 42\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run()
	if row := status(); row.Result != "failed" || row.State != "not_installed" || !row.Allowed || !strings.Contains(row.Message, "42") {
		t.Fatalf("部分安装应能重试：%+v", row)
	}
	if err := os.WriteFile(adb, []byte("#!/bin/sh\n[ \"$1\" = version ]\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run()
	if row := status(); row.Result != "succeeded" || row.State != "installed" || row.Version != "2.0.0" {
		t.Fatalf("安装状态或版本不正确：%+v", row)
	}
	calls, err := os.ReadFile(filepath.Join(s.brewPrefix, "calls"))
	if err != nil || strings.Count(string(calls), "install --formula mobile-dev-inc/tap/maestro") != 1 {
		t.Fatalf("重试重复安装已有配方：%s, %v", calls, err)
	}
	receipt := filepath.Join(s.brewPrefix, "opt", "maestro", "INSTALL_RECEIPT.json")
	if err := os.WriteFile(receipt, []byte(`{"source":{"tap":"other/tap"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if row := status(); row.Allowed || row.BlockedReason != "native_formula_conflict" {
		t.Fatalf("错误来源的同名配方被接受：%+v", row)
	}
}

func TestMaestroInstallerRejectsIncompatibleRequests(t *testing.T) {
	s, req := maestroFixture(t)
	ctx := context.Background()
	allow := func() bool { return true }
	if _, err := s.Start(ctx, testController, "install", req, func() bool { return false }); !errors.Is(err, ErrDenied) {
		t.Fatalf("无配对授权请求被接受：%v", err)
	}
	for _, invalid := range []Request{
		{Ref: req.Ref, NativeInstaller: "other"},
		{Ref: Ref{Source: testSource, App: "other"}, NativeInstaller: req.NativeInstaller},
		{Ref: req.Ref, NativeInstaller: req.NativeInstaller, Config: map[string]string{"cmd": "other"}},
		{Ref: req.Ref, NativeInstaller: req.NativeInstaller, Digest: strings.Repeat("a", 64)},
	} {
		if _, err := s.Start(ctx, testController, "install", invalid, allow); err == nil {
			t.Fatal("接受了非法安装请求")
		}
	}
	for _, action := range []string{"update", "uninstall"} {
		if _, err := s.Start(ctx, testController, action, req, allow); err == nil {
			t.Fatalf("接受非法动作 %s", action)
		}
	}
	if s.supportsMaestro() {
		if err := s.save(Record{Ref: req.Ref, Mode: directDeployMode, Managed: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Start(ctx, testController, "install", req, allow); !errors.Is(err, ErrConflict) {
			t.Fatalf("未拒绝既有 Compose 身份：%v", err)
		}
	}
	s.platform = "linux"
	if len(s.NativeInstallers()) != 0 {
		t.Fatal("Linux 不应声明 macOS 原生安装能力")
	}
	if _, err := s.Start(ctx, testController, "install", req, allow); err == nil {
		t.Fatal("不支持的系统接受了安装")
	}
}
