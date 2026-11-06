package market

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

const testSource = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const testController = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func cacheFixture(t *testing.T, store *bundles.Store, id string, requiresRoot ...bool) string {
	t.Helper()
	rootRequired := len(requiresRoot) > 0 && requiresRoot[0]
	manifest := fmt.Sprintf(`id: %s
runtime: {kind: docker_compose, compose_project: %s, compose_file: deploy/compose.yaml, requires_root: %t}
compatibility: {platforms: [linux, fnos, macos], architectures: [amd64, arm64]}
actions:
  install: {strategy: script, path: deploy/service.sh, argv: [install]}
  update: {strategy: script, path: deploy/service.sh, argv: [update]}
  uninstall: {strategy: script, path: deploy/service.sh, argv: [uninstall]}
config:
  - {key: WEB_PORT, type: port, target: env, default: 9000, required: true}
  - {key: APP_TOKEN, type: secret, target: secrets, required: false}
data: {default_retain: true}
verification: {required_services: [web]}
`, id, id, rootRequired)
	var buf bytes.Buffer
	zipper := zip.NewWriter(&buf)
	put := func(name, body string, mode os.FileMode) {
		h := &zip.FileHeader{Name: name}
		h.SetMode(mode)
		w, err := zipper.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	sum := func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
	info := bundles.CandidateInfo{FormatVersion: 1, ApplicationID: id, SourceID: testSource, GitRevision: strings.Repeat("1", 40), ManifestSHA256: sum([]byte(manifest))}
	put(id+"/app.yaml", manifest, 0644)
	for _, f := range []struct {
		file, body, mode string
		perm             os.FileMode
	}{
		{"compose.yaml", "services: {web: {image: example}}\n", "0644", 0644},
		{"service.sh", `set -eu
case "$1" in
  install) mkdir -p ../data; touch ../data/keep; touch ../online ;;
  update) test -f ../online; printf 'updated' > ../updated ;;
  uninstall) test ! -e ../data/refuse || exit 19; rm -f ../online ;;
  *) exit 77 ;;
esac
`, "0755", 0755},
	} {
		p := id + "/deploy/" + f.file
		put(p, f.body, f.perm)
		info.Files = append(info.Files, bundles.CandidateFile{Source: p, Destination: p, Mode: f.mode, SizeBytes: int64(len(f.body)), SHA256: sum([]byte(f.body))})
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	put("bundle-manifest.json", string(raw), 0644)
	if err = zipper.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sum(buf.Bytes())
	if _, _, err := store.Put(context.Background(), digest, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	return digest
}

// 使用两个无业务含义的应用和真正的 Bash 入口；此处只替换 Docker 发现，单独控制其成功和失败。
func TestTwoAppsLifecycle(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	data := filepath.Join(tmp, "agent")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(data)
	if err != nil {
		t.Fatal(err)
	}
	refs := []Ref{{testSource, "demo-reader"}, {testSource, "demo-writer"}}
	root := filepath.Join(tmp, "software")
	appRoot := func(ref Ref) string { return filepath.Join(root, ".easyhostagent-apps", ref.Source, ref.App) }
	scans := 0
	online := true
	scan := func(context.Context) (dockerengine.GroupedInventory, error) {
		scans++
		snap := dockerengine.GroupedInventory{Complete: online}
		for _, ref := range refs {
			dir := filepath.Join(appRoot(ref), ref.App)
			if _, err := os.Stat(filepath.Join(dir, "online")); err == nil {
				snap.Resources = append(snap.Resources, dockerengine.Resource{ComposeProject: ref.App, Services: []string{"web"}, Status: "running", Containers: []dockerengine.Container{{Labels: map[string]string{"com.docker.compose.project.working_dir": filepath.Join(dir, "deploy")}}}})
			}
		}
		return snap, nil
	}
	newService := func() *Service {
		s, err := New(ctx, config.Config{DataDir: data, SoftwareRoot: root, Platform: "linux"}, testController+"/"+testSource, cache, scan)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := newService()
	granted := func() bool { return true }
	finish := func(id string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			r, err := s.read(id)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if r.Result == "succeeded" {
				return
			}
			if r.Result != "running" {
				t.Fatalf("%s: %+v", id, r)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("脚本超时")
	}
	secret := "空 格'\"$!#中文"
	for _, ref := range refs {
		req := Request{Ref: ref, Digest: cacheFixture(t, cache, ref.App), Config: map[string]string{"APP_TOKEN": secret}}
		if _, err = s.Start(ctx, "wrong", "install", req, granted); !errors.Is(err, ErrDenied) {
			t.Fatal("未授权来源可执行")
		}
		req.Config["ARBITRARY"] = "no"
		if _, err = s.Start(ctx, testController, "install", req, granted); err == nil {
			t.Fatal("接受了未声明字段")
		}
		delete(req.Config, "ARBITRARY")
		if _, err = s.Start(ctx, testController, "install", req, granted); err != nil {
			t.Fatal(err)
		}
		finish(ref.App)
		c, err := readConfig(filepath.Join(appRoot(ref), "deploy", ".secrets"))
		if err != nil || c.values["APP_TOKEN"] != secret {
			t.Fatal("密钥未按字面值保存")
		}
	}
	// Update must call the declared Deploy update script, reuse persisted
	// secrets and never accept new config values without an explicit install.
	updatedRef := refs[0]
	updatedDigest := cacheFixture(t, cache, updatedRef.App)
	if _, err := s.Start(ctx, testController, "update",
		Request{Ref: updatedRef, Digest: updatedDigest, Config: map[string]string{"WEB_PORT": "9999"}}, granted); err == nil {
		t.Fatal("更新请求不得覆盖已有安装配置")
	}
	updatedSecretPath := filepath.Join(appRoot(updatedRef), "deploy", ".secrets")
	beforeSecret, err := os.ReadFile(updatedSecretPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, testController, "update",
		Request{Ref: updatedRef, Digest: updatedDigest}, granted); err != nil {
		t.Fatal("已安装应用无法启动更新脚本", err)
	}
	s.jobs.Wait()
	updatedRecord, err := s.read(updatedRef.App)
	if err != nil || updatedRecord.Action != "update" || updatedRecord.Result != "succeeded" {
		t.Fatalf("更新任务结果异常：%+v %v", updatedRecord, err)
	}
	if content, err := os.ReadFile(filepath.Join(appRoot(updatedRef), updatedRef.App, "updated")); err != nil || string(content) != "updated" {
		t.Fatal("更新未执行 Deploy 清单指定的 update 动作", err)
	}
	afterSecret, err := os.ReadFile(updatedSecretPath)
	if err != nil || !bytes.Equal(beforeSecret, afterSecret) {
		t.Fatal("更新不得改写已保存的凭据", err)
	}
	before := scans
	rows, err := s.Status(ctx, testController, refs, false)
	if err != nil || scans != before+1 || len(rows) != 2 || rows[0].State != "running" || rows[1].State != "running" {
		t.Fatal("批量状态未复用一次发现", err)
	}
	online = false
	rows, err = s.Status(ctx, testController, refs, false)
	if err != nil || rows[0].State != "unknown" {
		t.Fatal("不完整快照被当作已卸载")
	}
	online = true
	s = newService()
	rows, err = s.Status(ctx, testController, nil, true)
	if err != nil || len(rows) != 2 {
		t.Fatal("无法从本机恢复已安装应用")
	}
	refuse := filepath.Join(appRoot(refs[0]), refs[0].App, "data", "refuse")
	if err := os.WriteFile(refuse, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Start(ctx, testController, "uninstall", Request{Ref: refs[0]}, granted); err != nil {
		t.Fatal(err)
	}
	s.jobs.Wait()
	rows, err = s.Status(ctx, testController, refs, false)
	if err != nil || rows[0].Result != "failed" || !strings.Contains(rows[0].Message, "19") || rows[0].State != "running" || !rows[0].Managed {
		t.Fatal("卸载失败后未保留原状态和退出码")
	}
	if err = os.Remove(refuse); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Start(ctx, testController, "uninstall", Request{Ref: refs[0]}, granted); err != nil {
		t.Fatal(err)
	}
	finish(refs[0].App)
	rows, err = s.Status(ctx, testController, refs, false)
	if err != nil || rows[0].State != "not_installed" || rows[0].Managed || rows[1].State != "running" {
		t.Fatal("卸载影响范围或结果错误")
	}
	if _, err := os.Stat(filepath.Join(appRoot(refs[0]), refs[0].App, "data", "keep")); err != nil {
		t.Fatal("卸载删除了数据")
	}
	if _, err := s.Start(ctx, testController, "update", Request{
		Ref: refs[0], Digest: cacheFixture(t, cache, refs[0].App),
	}, granted); err == nil {
		t.Fatal("未安装的应用不能更新")
	}
}

func TestMacOSMarketInstallUpdateUninstall(t *testing.T) {
	ctx := context.Background()
	data := filepath.Join(t.TempDir(), "agent")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(data)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "software")
	scan := func(context.Context) (dockerengine.GroupedInventory, error) {
		return dockerengine.GroupedInventory{Complete: true}, nil
	}
	s, err := New(ctx, config.Config{DataDir: data, SoftwareRoot: root, Platform: "macos"}, "", cache, scan)
	if err != nil {
		t.Fatal(err)
	}
	ref := Ref{Source: testSource, App: "demo-macos"}
	digest := cacheFixture(t, cache, ref.App, true)
	authorized := func() bool { return true }

	if _, err := s.Start(ctx, testController, "install", Request{Ref: ref, Digest: digest}, authorized); err != nil {
		t.Fatalf("macOS 当前用户不能安装声明 requires_root 的 Docker Desktop 应用：%v", err)
	}
	s.Wait()
	record, err := s.read(ref.App)
	if err != nil || record.Result != "succeeded" || !record.Managed {
		t.Fatalf("macOS 安装结果错误：%+v %v", record, err)
	}
	if _, err := s.Start(ctx, testController, "update", Request{Ref: ref, Digest: digest}, authorized); err != nil {
		t.Fatalf("macOS 当前用户不能更新应用：%v", err)
	}
	s.Wait()
	record, err = s.read(ref.App)
	if err != nil || record.Action != "update" || record.Result != "succeeded" {
		t.Fatalf("macOS 更新结果错误：%+v %v", record, err)
	}
	if _, err := s.Start(ctx, testController, "uninstall", Request{Ref: ref}, authorized); err != nil {
		t.Fatalf("macOS 当前用户不能卸载应用：%v", err)
	}
	s.Wait()
	record, err = s.read(ref.App)
	if err != nil || record.Action != "uninstall" || record.Result != "succeeded" || record.Managed {
		t.Fatalf("macOS 卸载结果错误：%+v %v", record, err)
	}
}

func TestMacOSScriptEnvironmentUsesDockerDesktopSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := scriptEnvironment("macos")
	joined := strings.Join(env, "\n")
	wantSocket := "DOCKER_HOST=unix://" + filepath.Join(home, ".docker", "run", "docker.sock")
	for _, want := range []string{
		"EASYHOSTAGENT_PLATFORM=macos",
		wantSocket,
		filepath.Join(home, ".docker", "bin"),
		"/opt/homebrew/bin",
		"/Applications/Docker.app/Contents/Resources/bin",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("macOS 脚本环境缺少 %q：%s", want, joined)
		}
	}
}
