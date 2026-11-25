package market

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

const maestroTestFormula = "mobile-dev-inc/tap/maestro"
const maestroTestOrigin = "https://github.com/mobile-dev-inc/maestro/releases/download/cli-2.0.0/maestro.zip"
const maestroTestArchive = "maestro-fixture-2.0.0\n"

func maestroTestDigest(data string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

func maestroTestCache(prefix string) string {
	return filepath.Join(prefix, "cache with spaces", "downloads", maestroTestDigest(maestroTestOrigin)+"--maestro.zip")
}

func maestroTestWrite(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func maestroTestMetadata(t *testing.T, prefix, formula, url, checksum string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"formulae": []any{map[string]any{
		"full_name": formula, "tap": "mobile-dev-inc/tap",
		"urls": map[string]any{"stable": map[string]string{"url": url, "checksum": checksum}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	maestroTestWrite(t, filepath.Join(prefix, "metadata.json"), string(raw), 0600)
}

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
	if err := os.MkdirAll(filepath.Dir(maestroTestCache(s.brewPrefix)), 0700); err != nil {
		t.Fatal(err)
	}
	tap := filepath.Join(s.brewPrefix, "tap")
	for _, args := range [][]string{{"init", "--quiet", tap}, {"-C", tap, "remote", "add", "origin", "https://github.com/mobile-dev-inc/homebrew-tap.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("准备临时 tap 失败：%s：%v", out, err)
		}
	}
	maestroTestMetadata(t, s.brewPrefix, maestroTestFormula, maestroTestOrigin, maestroTestDigest(maestroTestArchive))
	maestroTestWrite(t, filepath.Join(s.brewPrefix, "archive"), maestroTestArchive, 0600)
	maestroTestWrite(t, filepath.Join(s.brewPrefix, "expected-cache"), maestroTestCache(s.brewPrefix), 0600)
	maestroTestWrite(t, filepath.Join(s.brewPrefix, "responses"), "ok\n", 0600)
	// 不访问网络。按请求顺序模拟传输中断、完整但校验错误的文件或有效发布包。
	const curl = `#!/bin/bash
set -eu
p="$(dirname "$(dirname "$0")")"
url="${!#}"
printf '%s\n' "$url" >> "$p/requests"
output=
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o|--output) output="$2"; shift 2 ;;
        --head|-I) echo '测试只支持实际 GET 下载' >&2; exit 55 ;;
        *) shift ;;
    esac
done
[[ -n "$output" ]]
n="$(wc -l < "$p/requests" | tr -d ' ')"
case "$(sed -n "${n}p" "$p/responses")" in
    ok) cp "$p/archive" "$output" ;;
    corrupt) printf '错误归档' > "$output" ;;
    fail) printf '中断的响应' > "$output"; exit 56 ;;
    *) echo '未预期的下载' >&2; exit 57 ;;
esac
`
	maestroTestWrite(t, filepath.Join(bin, "curl"), curl, 0755)
	// 模拟 Homebrew 收据与链接；安装命令必须看到与发布包一致的最终缓存。
	const brew = `#!/bin/bash
set -eu
p="$(dirname "$(dirname "$0")")"
echo "$*" >> "$p/calls"
case "$*" in
'list --formula --full-name')
    if [[ -e "$p/opt/maestro" ]]; then echo mobile-dev-inc/tap/maestro; fi ;;
'tap mobile-dev-inc/tap') ;;
'--repo mobile-dev-inc/tap') echo "$p/tap" ;;
'command trust') [[ ! -e "$p/no-trust" ]] && echo "$p/trust" ;;
'trust --formula mobile-dev-inc/tap/maestro') [[ ! -e "$p/no-trust" ]] ;;
'info --json=v2 --formula mobile-dev-inc/tap/maestro') cat "$p/metadata.json" ;;
'--cache') echo "$p/cache with spaces" ;;
'install --formula mobile-dev-inc/tap/maestro')
    cmp "$p/archive" "$(cat "$p/expected-cache")" || exit 41
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

var maestroTestSources = []string{
	"https://gh-proxy.org/" + maestroTestOrigin,
	"https://ghfast.top/" + maestroTestOrigin,
	"https://ghproxy.net/" + maestroTestOrigin,
	maestroTestOrigin,
}

func maestroShellRun(t *testing.T, s *Service, wantSuccess bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", maestroInstallScript, "maestro-install", s.brewPrefix, s.records)
	cmd.Env = []string{"HOME=" + s.brewPrefix, "PATH=" + filepath.Join(s.brewPrefix, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin", "NONINTERACTIVE=1"}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("执行超时：%s", output)
	}
	if (err == nil) != wantSuccess {
		t.Fatalf("执行 Maestro 安装脚本结果不符，期望成功=%t：%v\n%s", wantSuccess, err, output)
	}
}

func maestroTestLog(t *testing.T, prefix, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(prefix, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func maestroCheckSources(t *testing.T, prefix string, expected []string) {
	t.Helper()
	actual := maestroTestLog(t, prefix, "requests")
	if !slices.Equal(actual, expected) {
		t.Fatalf("下载请求不符：实际 %q，期望 %q", actual, expected)
	}
}

func maestroCheckNoInstall(t *testing.T, prefix string) {
	t.Helper()
	calls := maestroTestLog(t, prefix, "calls")
	if slices.Contains(calls, "install --formula "+maestroTestFormula) {
		t.Fatalf("无有效归档仍尝试安装配方：%q", calls)
	}
}

func TestMaestroShellVerifiesDownloadBeforeBrewInstall(t *testing.T) {
	for _, tc := range []struct {
		name           string
		responses      []string
		verifiedCache  bool
		trustAvailable bool
	}{
		{"首站传输失败且次站校验错误", []string{"fail", "corrupt", "ok"}, false, true},
		{"三个镜像失败后使用官方", []string{"corrupt", "fail", "fail", "ok"}, false, true},
		{"复用已校验缓存", nil, true, true},
		{"旧版Homebrew无trust命令", []string{"ok"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := maestroFixture(t)
			if tc.verifiedCache {
				maestroTestWrite(t, maestroTestCache(s.brewPrefix), maestroTestArchive, 0600)
			}
			if !tc.trustAvailable {
				maestroTestWrite(t, filepath.Join(s.brewPrefix, "no-trust"), "", 0600)
			}
			maestroTestWrite(t, filepath.Join(s.brewPrefix, "responses"), strings.Join(tc.responses, "\n"), 0600)
			maestroShellRun(t, s, true)
			maestroCheckSources(t, s.brewPrefix, maestroTestSources[:len(tc.responses)])
			calls := maestroTestLog(t, s.brewPrefix, "calls")
			for _, command := range []string{"tap mobile-dev-inc/tap", "info --json=v2 --formula " + maestroTestFormula, "--cache", "install --formula " + maestroTestFormula} {
				if !slices.Contains(calls, command) {
					t.Fatalf("缺少 Homebrew 调用 %q：%q", command, calls)
				}
			}
			if slices.Contains(calls, "trust --formula "+maestroTestFormula) != tc.trustAvailable {
				t.Fatalf("Homebrew trust 调用与命令支持情况不一致：%q", calls)
			}
			if tc.verifiedCache {
				// 命中缓存不轮换首选站。保留 Agent 记录，使用新的空缓存触发首次下载。
				next, _ := maestroFixture(t)
				next.records = s.records
				maestroShellRun(t, next, true)
				maestroCheckSources(t, next.brewPrefix, maestroTestSources[:1])
			}
		})
	}
}

func TestMaestroShellPreservesCacheAndRotatesAfterFailure(t *testing.T) {
	s, _ := maestroFixture(t)
	cache := maestroTestCache(s.brewPrefix)
	const old = "上次留下的旧归档"
	maestroTestWrite(t, cache, old, 0600)
	maestroTestWrite(t, filepath.Join(s.brewPrefix, "responses"), "fail\ncorrupt\nfail\ncorrupt\nok\n", 0600)
	maestroShellRun(t, s, false)
	maestroCheckSources(t, s.brewPrefix, maestroTestSources)
	maestroCheckNoInstall(t, s.brewPrefix)
	raw, err := os.ReadFile(cache)
	if err != nil || string(raw) != old {
		t.Fatalf("所有下载源失败后原缓存被改变：%q，%v", raw, err)
	}

	maestroShellRun(t, s, true)
	expected := append(slices.Clone(maestroTestSources), maestroTestSources[1])
	maestroCheckSources(t, s.brewPrefix, expected)
}

func TestMaestroShellRejectsInvalidReleaseBeforeGET(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		formula, url, checksum string
	}{
		{"缺少配方身份", "", maestroTestOrigin, maestroTestDigest(maestroTestArchive)},
		{"错误配方身份", "other/tap/maestro", maestroTestOrigin, maestroTestDigest(maestroTestArchive)},
		{"缺少发布地址", maestroTestFormula, "", maestroTestDigest(maestroTestArchive)},
		{"替换域名", maestroTestFormula, strings.Replace(maestroTestOrigin, "github.com/", "github.com.example.org/", 1), maestroTestDigest(maestroTestArchive)},
		{"替换发布仓库", maestroTestFormula, strings.Replace(maestroTestOrigin, "mobile-dev-inc/", "other/", 1), maestroTestDigest(maestroTestArchive)},
		{"缺少校验和", maestroTestFormula, maestroTestOrigin, ""},
		{"非法校验和", maestroTestFormula, maestroTestOrigin, strings.Repeat("z", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := maestroFixture(t)
			maestroTestMetadata(t, s.brewPrefix, tc.formula, tc.url, tc.checksum)
			maestroShellRun(t, s, false)
			maestroCheckSources(t, s.brewPrefix, nil)
			maestroCheckNoInstall(t, s.brewPrefix)
		})
	}
}

func TestMaestroShellRejectsForeignTap(t *testing.T) {
	s, _ := maestroFixture(t)
	if out, err := exec.Command("git", "-C", filepath.Join(s.brewPrefix, "tap"), "remote", "set-url", "origin", "https://github.com/other/homebrew-tap.git").CombinedOutput(); err != nil {
		t.Fatalf("设置测试 tap 来源失败：%s：%v", out, err)
	}
	maestroShellRun(t, s, false)
	maestroCheckSources(t, s.brewPrefix, nil)
	maestroCheckNoInstall(t, s.brewPrefix)
}

func TestMaestroShellDoesNotDownloadAgainAfterADBFailure(t *testing.T) {
	s, _ := maestroFixture(t)
	adb := filepath.Join(s.brewPrefix, "bin", "adb")
	maestroTestWrite(t, adb, "#!/bin/sh\nexit 42\n", 0755)
	maestroShellRun(t, s, false)
	maestroCheckSources(t, s.brewPrefix, maestroTestSources[:1])
	// 清掉已安装包的旧下载副本，防止把重试成功误判为命中缓存。
	if err := os.Remove(maestroTestCache(s.brewPrefix)); err != nil {
		t.Fatal(err)
	}
	maestroTestWrite(t, adb, "#!/bin/sh\n[ \"$1\" = version ]\n", 0755)
	maestroShellRun(t, s, true)
	maestroCheckSources(t, s.brewPrefix, maestroTestSources[:1])
	calls := maestroTestLog(t, s.brewPrefix, "calls")
	if strings.Count(strings.Join(calls, "\n"), "install --formula "+maestroTestFormula) != 1 {
		t.Fatalf("重试 ADB 时重复安装 Maestro：%q", calls)
	}
}
