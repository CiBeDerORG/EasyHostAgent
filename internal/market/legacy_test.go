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

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

func legacyBundle(t *testing.T, store *bundles.Store, app string) string {
	t.Helper()
	manifest := fmt.Sprintf(`id: %s
runtime: {kind: docker_compose, compose_project: %s, compose_file: deploy/docker-compose.yaml}
compatibility: {platforms: [linux, fnos], architectures: [amd64, arm64]}
actions:
  install: {strategy: script, path: deploy/install.sh}
  update: {strategy: script, path: deploy/update.sh}
  uninstall: {strategy: script, path: deploy/uninstall.sh}
data: {default_retain: true}
verification: {required_services: [api]}
`, app, app)
	sha := func(v []byte) string { return fmt.Sprintf("%x", sha256.Sum256(v)) }
	info := bundles.CandidateInfo{
		FormatVersion: 1, SourceID: testSource, ApplicationID: app,
		GitRevision: strings.Repeat("a", 40), ManifestSHA256: sha([]byte(manifest)),
	}
	var buf bytes.Buffer
	zipper := zip.NewWriter(&buf)
	write := func(name, body, mode string) {
		h := &zip.FileHeader{Name: name}
		if mode == "0755" {
			h.SetMode(0755)
		} else {
			h.SetMode(0644)
		}
		w, err := zipper.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if name != app+"/app.yaml" {
			info.Files = append(info.Files, bundles.CandidateFile{
				Source: name, Destination: name, Mode: mode, SizeBytes: int64(len(body)), SHA256: sha([]byte(body)),
			})
		}
	}
	write(app+"/app.yaml", manifest, "0644")
	write(app+"/deploy/docker-compose.yaml", "services: {api: {image: example}}\n", "0644")
	for _, action := range []string{"install", "update", "uninstall"} {
		write(app+"/deploy/"+action+".sh", "echo ok\n", "0755")
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	write("bundle-manifest.json", string(raw), "0644")
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha(buf.Bytes())
	if _, _, err := store.Put(context.Background(), digest, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestLegacyDeployAdoptionRequiresGrantAndExactComposeIdentity(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "software")
	work := filepath.Join(root, "sub2api", "deploy")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yaml", "install.sh", "update.sh", "uninstall.sh"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("echo ok\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(base, "agent-data")
	cache, err := bundles.New(data)
	if err != nil {
		t.Fatal(err)
	}
	ref := Ref{Source: testSource, App: "sub2api"}
	digest := legacyBundle(t, cache, ref.App)
	project := dockerengine.Resource{
		ComposeProject: ref.App, Status: "running",
		Containers: []dockerengine.Container{{Labels: map[string]string{
			"com.docker.compose.project":              ref.App,
			"com.docker.compose.service":              "api",
			"com.docker.compose.project.working_dir":  work,
			"com.docker.compose.project.config_files": filepath.Join(work, "docker-compose.yaml"),
		}}},
	}
	scan := func(context.Context) (dockerengine.GroupedInventory, error) {
		return dockerengine.GroupedInventory{Complete: true, Resources: []dockerengine.Resource{project}}, nil
	}
	cfg := config.Config{SoftwareRoot: root, DataDir: data, Platform: "linux"}
	grant := testController + "/" + testSource + "/sub2api"
	agent, err := New(ctx, cfg, grant, cache, scan)
	if err != nil {
		t.Fatal(err)
	}
	if agent.hasGrant(testController, Ref{Source: testSource, App: "portainer"}) {
		t.Fatal("sub2api 授权不应自动允许 Portainer")
	}
	rows, err := agent.Status(ctx, testController, []Ref{ref}, false)
	if err != nil || len(rows) != 1 || !rows[0].Adoptable || !rows[0].External ||
		rows[0].BlockedReason != "adoption_required" {
		t.Fatalf("真实原 Deploy 容器应可明确选择接管: %+v %v", rows, err)
	}
	if _, err := agent.Adopt(ctx, "unknown", Request{Ref: ref, Digest: digest}, func() bool { return true }); !errors.Is(err, ErrDenied) {
		t.Fatal("不得绕过 App 授权接管外部应用")
	}
	if _, err := agent.Adopt(ctx, testController, Request{Ref: ref, Digest: digest}, func() bool { return false }); !errors.Is(err, ErrDenied) {
		t.Fatal("不得绕过 Agent 配对授权")
	}
	project.Containers[0].Labels["com.docker.compose.project.working_dir"] = filepath.Join(root, "unrelated", "deploy")
	if _, err := agent.Adopt(ctx, testController, Request{Ref: ref, Digest: digest}, func() bool { return true }); !errors.Is(err, ErrConflict) {
		t.Fatalf("外部工作目录不匹配时必须禁止接管: %v", err)
	}
	project.Containers[0].Labels["com.docker.compose.project.working_dir"] = work
	before, err := os.ReadFile(filepath.Join(work, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := agent.Adopt(ctx, testController, Request{Ref: ref, Digest: digest}, func() bool { return true })
	if err != nil || !accepted.Managed || accepted.Mode != legacyDeployMode || accepted.Action != "adopt" {
		t.Fatalf("原部署接管失败: %+v %v", accepted, err)
	}
	after, err := os.ReadFile(filepath.Join(work, "docker-compose.yaml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("接管不应改动原有 Compose 文件")
	}
	rows, err = agent.Status(ctx, testController, []Ref{ref}, false)
	if err != nil || len(rows) != 1 || !rows[0].Managed || rows[0].External || rows[0].Mode != legacyDeployMode {
		t.Fatalf("接管后应正常允许受管应用操作: %+v %v", rows, err)
	}
	// Existing scripts are exercised in the legacy directory, without
	// materializing a second copy under .easyhostagent-apps.
	if err := os.WriteFile(filepath.Join(work, "update.sh"), []byte("printf updated > ../updated\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Start(ctx, testController, "update", Request{Ref: ref, Digest: digest}, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	agent.jobs.Wait()
	if out, err := os.ReadFile(filepath.Join(root, "sub2api", "updated")); err != nil || string(out) != "updated" {
		t.Fatalf("更新未调用原 Deploy 目录脚本: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".easyhostagent-apps")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("接管的旧应用不应新建独立安装工作区")
	}
	// Simulate stopped Docker containers being replaced by the original
	// Deploy update script; the binding remains but mismatched labels block.
	project.Containers[0].Labels["com.docker.compose.project.config_files"] = "/tmp/untrusted-compose.yaml"
	rows, err = agent.Status(ctx, testController, []Ref{ref}, false)
	if err != nil || !rows[0].External || rows[0].Allowed || rows[0].BlockedReason != "legacy_identity_mismatch" {
		t.Fatalf("路径漂移后操作必须锁定: %+v %v", rows, err)
	}
}

func TestLegacyGrantParsingAndBackwardCompatibility(t *testing.T) {
	base := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(base, "data"), SoftwareRoot: filepath.Join(base, "software"), Platform: "linux"}
	for _, grant := range []string{
		"nonsense", testController + "/" + testSource + "/",
		testController + "/" + testSource + "/sub2api/extra",
	} {
		if _, err := New(context.Background(), cfg, grant, nil, nil); err == nil {
			t.Fatalf("授权格式应拒绝: %s", grant)
		}
	}
	apps := []Ref{{testSource, "sub2api"}, {testSource, "portainer"}, {testSource, "qoder2api-hub"}}
	for _, tc := range []struct {
		grant string
		want  [3]bool
	}{
		{testController + "/" + testSource, [3]bool{true, true, true}},
		{testController + "/" + testSource + "/sub2api," +
			testController + "/" + testSource + "/portainer", [3]bool{true, true, false}},
	} {
		s, err := New(context.Background(), cfg, tc.grant, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, app := range apps {
			if s.hasGrant(testController, app) != tc.want[i] {
				t.Fatalf("应用授权范围错误: %+v, %s", app, tc.grant)
			}
		}
	}
}
