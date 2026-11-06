package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestMacOSDockerRoutesAreProtectedAndMarketRemainsDisabled(t *testing.T) {
	state, err := identity.LoadOrCreate(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(filepath.Join(t.TempDir(), "pairing"), state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := dockerengine.New(filepath.Join(t.TempDir(), "offline.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	handler := NewHandlerWithDocker(state, "macos", "phase2", nil, engine, store)
	call := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://localhost"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if response := call("/v1/docker/inventory"); response.Code != http.StatusUnauthorized {
		t.Fatalf("Mac Docker API 不得未经授权访问：%d", response.Code)
	}
	if response := call("/v1/docker/resources/container:test/logs"); response.Code != http.StatusUnauthorized {
		t.Fatalf("Mac Docker 日志不得未经授权读取：%d", response.Code)
	}
	if response := call("/v1/apps/status"); response.Code != http.StatusNotFound {
		t.Fatalf("macOS 应用商店执行器必须继续关闭：%d", response.Code)
	}
	response := call("/v1/identity")
	var body struct {
		ManifestVersions []int  `json:"app_manifest_versions"`
		Platform         string `json:"platform"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK ||
		body.Platform != "macos" || len(body.ManifestVersions) != 0 {
		t.Fatalf("macOS 身份不应声明应用执行权限：%+v %v", body, err)
	}
}
