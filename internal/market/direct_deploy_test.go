package market

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

func TestDeployUpdateRefreshesVersionFiles(t *testing.T) {
	root := t.TempDir()
	install := func(version string) {
		t.Helper()
		info := bundles.CandidateInfo{ApplicationID: "demo"}
		files := map[string][]byte{
			"deploy/lib/versions.env":     []byte("DEMO_IMAGE=demo:" + version),
			"deploy/lib/versions.sh":      []byte("# 版本读取器 " + version),
			"deploy/lib/app-config.sh":    []byte("# 配置读取器 " + version),
			"deploy/lib/image-sources.sh": []byte("# 镜像源 " + version),
			"demo/config/settings.json":   []byte(`{"template":"` + version + `"}`),
		}
		for dest, contents := range files {
			info.Files = append(info.Files, bundles.CandidateFile{Destination: dest, SizeBytes: int64(len(contents))})
		}
		if err := materializeDeploy(root, "linux", info, files); err != nil {
			t.Fatal(err)
		}
	}
	install("v1")
	if err := os.WriteFile(filepath.Join(root, "demo/config/settings.json"), []byte(`{"user":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	install("v2")
	install("v2")
	for path, want := range map[string]string{
		"deploy/lib/versions.env":     "DEMO_IMAGE=demo:v2",
		"deploy/lib/versions.sh":      "# 版本读取器 v2",
		"deploy/lib/app-config.sh":    "# 配置读取器 v2",
		"deploy/lib/image-sources.sh": "# 镜像源 v1",
		"demo/config/settings.json":   `{"user":true}`,
	} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Errorf("%s 更新后得到 %q，期望 %q，错误：%v", path, got, want, err)
		}
	}
}
