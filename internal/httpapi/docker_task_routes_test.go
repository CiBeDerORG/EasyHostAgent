package httpapi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockertasks"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestMacOSDockerTaskRequiresPairedController(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := dockerengine.New(filepath.Join(t.TempDir(), "offline.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	tasks, err := dockertasks.New(dir, state.AgentID, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	handler := NewHandlerWithDockerTasksTransport(state, "macos", "test", nil, engine, nil, nil, tasks, false, store)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	body := []byte(`{"task_id":"11111111-1111-4111-8111-111111111111","kind":"docker_resource","resource_id":"container:web","action":"stop"}`)
	send := func(method, path, controller, token string, payload []byte) int {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("X-EHA-Controller-ID", controller)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if got := send("POST", "/v1/tasks", controllerA, "bad", body); got != 401 {
		t.Fatalf("未配对控制端提交任务：%d", got)
	}
	code, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	response, value := postPair(t, server, `{"controller_id":"`+controllerA+`","device_id":"`+deviceA+`","pairing_code":"`+code+`"}`, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("测试配对失败：%+v", value)
	}
	token := value["access_token"].(string)
	if got := send("POST", "/v1/tasks", controllerB, token, body); got != 401 {
		t.Fatalf("其他控制端不得提交任务：%d", got)
	}
	if got := send("POST", "/v1/tasks", controllerA, token, body); got != http.StatusAccepted {
		t.Fatalf("已授权任务未持久化接受：%d", got)
	}
	if got := send("POST", "/v1/tasks", controllerA, token, body); got != http.StatusAccepted {
		t.Fatalf("相同任务重试未幂等：%d", got)
	}
	invalid := []byte(`{"task_id":"22222222-2222-4222-8222-222222222222","kind":"docker_resource","resource_id":"container:web","action":"delete"}`)
	if got := send("POST", "/v1/tasks", controllerA, token, invalid); got != 422 {
		t.Fatalf("禁止容器删除与未授权动作：%d", got)
	}
	if got := send("GET", "/v1/tasks", controllerA, token, nil); got != 200 {
		t.Fatalf("任务列表未开放给已授权控制端：%d", got)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/tasks/11111111-1111-4111-8111-111111111111", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-EHA-Controller-ID", controllerA)
	req.Header.Set("X-EHA-Agent-ID", state.AgentID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var record dockertasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil || record.AgentID != state.AgentID {
		t.Fatalf("任务查询未验证 Agent 身份：%+v %v", record, err)
	}
}
