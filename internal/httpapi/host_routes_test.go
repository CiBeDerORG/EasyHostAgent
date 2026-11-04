package httpapi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/host"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestHostRoutesUseControllerAuthorization(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := host.New(dir, filepath.Join(base, "software"), "linux", state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandlerWithHost(state, "linux", "test", svc, store))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	code, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
	res, data := postPair(t, server, body, "application/json")
	if res.StatusCode != 201 {
		t.Fatalf("pair: %v", data)
	}
	token := data["access_token"].(string)
	call := func(method, path, controller, bearer, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-EHA-Controller-ID", controller)
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, v
	}
	if code, _ := call("GET", "/v1/host", controllerA, "bad", ""); code != 401 {
		t.Fatalf("invalid token accepted: %d", code)
	}
	if code, _ := call("GET", "/v1/host", controllerB, token, ""); code != 401 {
		t.Fatalf("other controller accepted: %d", code)
	}
	// 自更新入口与普通管理 API 使用同一配对令牌，不接受 GET、未知动作或非当前控制端。
	if code, _ := call("POST", "/v1/host/maintenance/update", controllerB, token, ""); code != 401 {
		t.Fatalf("其他控制端不得下发自更新: %d", code)
	}
	if code, _ := call("POST", "/v1/host/maintenance/force-update", controllerA, "bad", ""); code != 401 {
		t.Fatalf("无效 Token 不得强制更新: %d", code)
	}
	if code, _ := call("POST", "/v1/host/maintenance/uninstall", controllerA, token, ""); code != 404 {
		t.Fatalf("自维护接口不得开放卸载: %d", code)
	}
	if code, _ := call("GET", "/v1/host/maintenance/update", controllerA, token, ""); code != 405 {
		t.Fatalf("自更新接口只接受 POST: %d", code)
	}
	if code, _ := call("POST", "/v1/host/maintenance/update", controllerA, token, `{"script":"rm -rf /"}`); code != 400 {
		t.Fatalf("自更新请求不得接受脚本参数: %d", code)
	}
	if code, _ := call("POST", "/v1/host/maintenance/update", controllerA, token, ""); code != 503 {
		t.Fatalf("缺少原生安装器时必须拒绝自更新: %d", code)
	}
	code1, snap := call("GET", "/v1/host", controllerA, token, "")
	if code1 != 200 || snap["agent_id"] != state.AgentID || snap["software_root"] != filepath.Join(base, "software") || snap["agent_started_at"] == "" {
		t.Fatalf("host snapshot: %d %v", code1, snap)
	}
	next := filepath.Join(base, "next")
	code1, snap = call("PATCH", "/v1/host/settings", controllerA, token, `{"software_root":"`+next+`"}`)
	if code1 != 200 || snap["software_root"] != next {
		t.Fatalf("patch: %d %v", code1, snap)
	}
	if code1, _ = call("PATCH", "/v1/host/settings", controllerA, token, `{"software_root":"relative"}`); code1 != 422 {
		t.Fatalf("relative root accepted: %d", code1)
	}
	if code1, _ = call("PATCH", "/v1/host/settings", controllerA, token, `{"software_root":"`+next+`","extra":true}`); code1 != 400 {
		t.Fatalf("unknown field accepted: %d", code1)
	}
	if err := os.Mkdir(filepath.Join(dir, "managed-apps"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "managed-apps", "one"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if code1, _ = call("PATCH", "/v1/host/settings", controllerA, token, `{"software_root":"`+filepath.Join(base, "again")+`"}`); code1 != 409 {
		t.Fatalf("managed app guard: %d", code1)
	}
	if code1, snap = call("GET", "/v1/host", controllerA, token, ""); code1 != 200 || snap["software_root"] != next {
		t.Fatalf("root changed after rejected PATCH: %d %v", code1, snap)
	}
}
