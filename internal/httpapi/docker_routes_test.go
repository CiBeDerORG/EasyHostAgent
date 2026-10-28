package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestDockerReadRoutesArePairedAndReadOnly(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	engineServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"web","Names":["/web"],"Image":"web:1","State":"running","Labels":{},"Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]}]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/web/logs":
			_, _ = w.Write([]byte("hello from container\n"))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = engineServer.Serve(listener) }()
	defer func() {
		_ = engineServer.Shutdown(context.Background())
		_ = listener.Close()
	}()
	engine, err := dockerengine.New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandlerWithDocker(state, "linux", "test", nil, engine, store))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	request := func(method, path, controller, bearer string) (int, map[string]any) {
		t.Helper()
		req, e := http.NewRequest(method, server.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("X-EHA-Controller-ID", controller)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var value map[string]any
		if e := json.NewDecoder(resp.Body).Decode(&value); e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, value
	}
	if status, _ := request("GET", "/v1/docker/inventory", controllerA, "invalid"); status != 401 {
		t.Fatalf("unauthorized Docker inventory exposed: %d", status)
	}
	code, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
	paired, result := postPair(t, server, body, "application/json")
	if paired.StatusCode != 201 {
		t.Fatalf("pairing failed: %v", result)
	}
	token := result["access_token"].(string)
	if status, _ := request("GET", "/v1/docker/inventory", controllerB, token); status != 401 {
		t.Fatalf("other controller read Docker inventory: %d", status)
	}
	status, inventory := request("GET", "/v1/docker/inventory", controllerA, token)
	items, ok := inventory["resources"].([]any)
	if status != 200 || inventory["complete"] != true || !ok || len(items) != 1 {
		t.Fatalf("wrong Docker inventory: %d %+v", status, inventory)
	}
	status, logs := request("GET", "/v1/docker/resources/container:web/logs?tail=20", controllerA, token)
	if status != 200 || logs["text"] == "" || logs["resource_id"] != "container:web" {
		t.Fatalf("Docker logs missing: %d %+v", status, logs)
	}
	if status, _ := request("GET", "/v1/docker/resources/container:web/logs?tail=1000", controllerA, token); status != 400 {
		t.Fatalf("invalid tail accepted: %d", status)
	}
	if status, _ := request("POST", "/v1/docker/inventory", controllerA, token); status != 405 {
		t.Fatalf("mutating inventory API exposed: %d", status)
	}
	if status, _ := request("POST", "/v1/docker/resources/container:web/actions", controllerA, token); status != 404 {
		t.Fatalf("unsafe synchronous Docker action route exposed: %d", status)
	}
}
