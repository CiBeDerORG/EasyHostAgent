package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/taskstore"
)

func TestTaskHistoryRequiresPairingAndRedactsSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	pairingStore, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := taskstore.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer tasks.Close()
	if _, _, err := tasks.Create(taskstore.TaskStart{TaskID: "223e4567-e89b-12d3-a456-426614174000", Kind: "application", Action: "install", ApplicationID: "portainer", Secrets: map[string]string{"PASSWORD": "never-return-this-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Transition("223e4567-e89b-12d3-a456-426614174000", taskstore.StatusRunning, "running", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AppendLog("223e4567-e89b-12d3-a456-426614174000", "never-return-"); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AppendLog("223e4567-e89b-12d3-a456-426614174000", "this-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Transition("223e4567-e89b-12d3-a456-426614174000", taskstore.StatusSucceeded, "ok", intPtrRoute(0), nil); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(NewHandlerWithTasks(state, "linux", "test", nil, nil, nil, tasks, pairingStore))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	unauthenticated, err := server.Client().Get(server.URL + "/v1/tasks/223e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("task history accepted an unpaired request: %d", unauthenticated.StatusCode)
	}
	code, _, err := pairingStore.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pairResponse, pairBody := postPair(t, server,
		`{"controller_id":"`+controllerA+`","device_id":"`+deviceA+`","pairing_code":"`+code+`"}`, "application/json")
	if pairResponse.StatusCode != http.StatusCreated {
		t.Fatalf("pairing failed: %+v", pairBody)
	}
	token := pairBody["access_token"].(string)
	get := func(path string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("X-EHA-Controller-ID", controllerA)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	status, body := get("/v1/tasks/223e4567-e89b-12d3-a456-426614174000")
	if status != http.StatusOK || body["status"] != "succeeded" {
		t.Fatalf("task query failed: %d %+v", status, body)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "never-return-this-secret") {
		t.Fatal("task response exposed secret material")
	}
	status, body = get("/v1/tasks/223e4567-e89b-12d3-a456-426614174000/logs?offset=0")
	if status != http.StatusOK || strings.Contains(body["text"].(string), "never-return-this-secret") ||
		!strings.Contains(body["text"].(string), "[REDACTED]") {
		t.Fatalf("task logs were not safely redacted: %d %+v", status, body)
	}
	status, body = get("/v1/tasks?limit=10")
	if status != http.StatusOK || len(body["items"].([]any)) != 1 {
		t.Fatalf("task list failed: %d %+v", status, body)
	}
}

func intPtrRoute(value int) *int { return &value }
