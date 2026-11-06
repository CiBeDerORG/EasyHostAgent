package market

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"gopkg.in/yaml.v3"
)

func boolPointer(v bool) *bool     { return &v }
func textPointer(v string) *string { return &v }

func TestApplicationConfigurationIsolationAndPolicies(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "deploy"), 0700); err != nil {
		t.Fatal(err)
	}
	original := " 原 值'\"中文 $() `x` "
	if err := os.WriteFile(filepath.Join(root, "deploy/.secrets"), []byte("APP_SECRET=\""+original+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var m manifest
	m.ID = "unregistered-demo"
	m.Version = 2
	m.Config = []field{
		{Key: "APP_PORT", Type: "port", Target: "env", Required: true, Default: textPointer("8123"), CustomInput: boolPointer(false)},
		{Key: "APP_READONLY", Type: "string", Target: "env", Default: textPointer("fixed"), CustomInput: boolPointer(true), Editable: boolPointer(false)},
		{Key: "APP_SECRET", Type: "secret", Target: "secrets", Default: textPointer(""), CustomInput: boolPointer(true)},
	}
	if err := os.Mkdir(filepath.Join(root, m.ID), 0700); err != nil {
		t.Fatal(err)
	}
	for _, input := range []map[string]string{{"APP_PORT": "9999"}, {"APP_READONLY": "changed"}, {"UNDECLARED": "value"}} {
		if _, err := prepareManifestConfig(root, "linux", m, input, true); err == nil {
			t.Fatal("接受了禁止输入的字段")
		}
	}
	values, err := prepareManifestConfig(root, "linux", m, map[string]string{"APP_SECRET": "replacement"}, true)
	if err != nil || values["APP_SECRET"] != original || values["APP_PORT"] != "8123" {
		t.Fatalf("迁移或默认值失败：%v", err)
	}
	path := filepath.Join(root, m.ID, "config/.secrets")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepareManifestConfig(root, "linux", m, nil, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("重试改变了已有密钥")
	}
	global, _ := os.ReadFile(filepath.Join(root, "deploy/.secrets"))
	if !strings.Contains(string(global), original) {
		t.Fatal("迁移删除了旧凭据")
	}
	if _, err := os.Stat(filepath.Join(root, "deploy/.env")); !os.IsNotExist(err) {
		t.Fatal("应用写入污染了全局配置")
	}
}

func TestIndependentFieldDefinitionsRejectAmbiguity(t *testing.T) {
	normal := []byte("fields:\n- key: APP_PORT\n  label: 端口\n  type: port\n  required: true\n  custom_input: false\n  default: 8123\n  description: 测试端口\n")
	for _, bad := range []string{
		"fields: []\nfields: []\n", "fields: []\n---\nfields: []\n",
		"fields: &fields []\nextra: *fields\n",
		"fields:\n- key: BAD_SECRET\n  type: secret\n  custom_input: true\n  default: forbidden\n",
	} {
		var m manifest
		m.ID = "demo"
		m.Version = 2
		m.Configuration.Config = "config.yaml"
		m.Configuration.Secrets = "secrets.yaml"
		if err := resolveManifestFields(&m, map[string][]byte{"demo/config.yaml": normal, "demo/secrets.yaml": []byte(bad)}); err == nil {
			t.Fatal("接受了有歧义的定义")
		}
	}
}

func TestV2ConfigurationLifecycle(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		name := "首次安装"
		if migrate {
			name = "从全局配置更新"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root, data := filepath.Join(base, "software"), filepath.Join(base, "agent")
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			cache, err := bundles.New(data)
			if err != nil {
				t.Fatal(err)
			}
			ref := Ref{Source: testSource, App: "unregistered-demo"}
			digest := legacyBundle(t, cache, ref.App, 2)
			work := filepath.Join(root, ref.App, "deploy")
			project := dockerengine.Resource{ComposeProject: ref.App, Status: "running", Containers: []dockerengine.Container{{Labels: map[string]string{
				"com.docker.compose.project":              ref.App,
				"com.docker.compose.service":              "api",
				"com.docker.compose.project.working_dir":  work,
				"com.docker.compose.project.config_files": filepath.Join(work, "docker-compose.yaml"),
			}}}}
			installed := false
			agent, err := New(ctx, config.Config{SoftwareRoot: root, DataDir: data, Platform: "linux"}, "", cache,
				func(context.Context) (dockerengine.GroupedInventory, error) {
					snapshot := dockerengine.GroupedInventory{Complete: true}
					if installed {
						snapshot.Resources = []dockerengine.Resource{project}
					}
					return snapshot, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			run := func(action, bundle string, input map[string]string) {
				t.Helper()
				if _, err := agent.Start(ctx, testController, action, Request{Ref: ref, Digest: bundle, Config: input}, func() bool { return true }); err != nil {
					t.Fatal(err)
				}
				agent.jobs.Wait()
				rec, err := agent.read(ref.App)
				if err != nil || rec.Result != "succeeded" {
					t.Fatalf("生命周期执行失败：%s %+v %v", action, rec, err)
				}
			}
			secret := " 保留'\"密钥 $() `x` "
			if migrate {
				run("install", legacyBundle(t, cache, ref.App), nil)
				if err := os.WriteFile(filepath.Join(root, "deploy/.secrets"), []byte("APP_SECRET=\""+secret+"\"\nOTHER_SECRET=other\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				run("install", digest, map[string]string{"APP_SECRET": secret})
			}
			installed = true
			run("update", digest, nil)
			privatePath := filepath.Join(root, ref.App, "config/.secrets")
			private, err := readConfig(privatePath)
			if err != nil || private.values["APP_SECRET"] != secret || len(private.values) != 1 {
				t.Fatal("更新未保留原密钥或混入其他应用密钥")
			}
			ordinary, err := readConfig(filepath.Join(root, ref.App, "config/.env"))
			if err != nil || ordinary.values["APP_PORT"] != "8123" {
				t.Fatal("隐藏字段默认值未落盘")
			}
			st, err := os.Stat(privatePath)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatal("密钥文件权限不正确")
			}
			run("uninstall", "", nil)
			private, err = readConfig(privatePath)
			if err != nil || private.values["APP_SECRET"] != secret {
				t.Fatal("卸载删除了密钥")
			}
			if migrate {
				global, err := readConfig(filepath.Join(root, "deploy/.secrets"))
				if err != nil || global.values["APP_SECRET"] != secret || global.values["OTHER_SECRET"] != "other" {
					t.Fatal("迁移修改了旧全局配置")
				}
			}
		})
	}
}

// 可选跨仓库契约回归，CI 可提供 Deploy checkout 路径。
func TestDeployRepositoryV2Contract(t *testing.T) {
	root := os.Getenv("DEPLOY_CONTRACT_ROOT")
	if root == "" {
		t.Skip("未指定 Deploy 工作区")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*/app.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatal("没有应用清单")
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var source struct {
				ID      string
				Package struct {
					Files []struct{ Source, Destination, Mode string }
				}
			}
			if err := yaml.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{source.ID + "/app.yaml": raw}
			info := bundles.CandidateInfo{ApplicationID: source.ID}
			for _, f := range source.Package.Files {
				content, err := os.ReadFile(filepath.Join(root, f.Source))
				if err != nil {
					t.Fatal(err)
				}
				files[f.Destination] = content
				info.Files = append(info.Files, bundles.CandidateFile{Source: f.Source, Destination: f.Destination, Mode: f.Mode})
			}
			m, err := loadManifest(info, files)
			if err != nil || m.Version != 2 || len(m.Config) == 0 {
				t.Fatalf("应用配置契约无法解析：%v", err)
			}
		})
	}
}
