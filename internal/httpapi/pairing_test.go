package httpapi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

const (
	controllerA = "22222222-2222-4222-8222-222222222222"
	controllerB = "44444444-4444-4444-8444-444444444444"
	deviceA     = "33333333-3333-4333-8333-333333333333"
)

func startPairedServer(t *testing.T) (*httptest.Server, *pairing.Store, identity.State) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandler(state, "linux", "test", store))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, store, state
}
func postPair(t *testing.T, server *httptest.Server, body string, ctype string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/pairings", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	return resp, data
}
func TestPairingHTTPAndRevocationOverRealTLS(t *testing.T) {
	server, store, state := startPairedServer(t)
	code, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	reqBody := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
	resp, data := postPair(t, server, reqBody, "application/json")
	if resp.StatusCode != 201 {
		t.Fatalf("pair failed: %d %v", resp.StatusCode, data)
	}
	token, ok := data["access_token"].(string)
	if !ok || len(token) != 64 {
		t.Fatal("missing valid one-time token")
	}
	if data["agent_id"] != state.AgentID {
		t.Fatal("agent ID mismatch")
	}
	if !store.Authorized(controllerA, state.AgentID, token) {
		t.Fatal("token not persisted")
	}
	resp, _ = postPair(t, server, reqBody, "application/json")
	if resp.StatusCode == 201 {
		t.Fatal("pairing code replay accepted")
	}
	revoke := func(ctrl, authToken string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/pairings/current", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-EHA-Controller-ID", ctrl)
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("Authorization", "Bearer "+authToken)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if status := revoke(controllerB, token); status != 401 {
		t.Fatalf("other controller can revoke: %d", status)
	}
	if status := revoke(controllerA, token); status != 204 {
		t.Fatalf("revoke: %d", status)
	}
	if store.Authorized(controllerA, state.AgentID, token) {
		t.Fatal("revoked grant still accepted")
	}
	if status := revoke(controllerA, token); status != 401 {
		t.Fatalf("token reusable after revoke: %d", status)
	}
}
func TestRateLimitAndJSONGuard(t *testing.T) {
	server, store, _ := startPairedServer(t)
	code, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + strings.Repeat("f", 32) + `"}`
	for i := 0; i < 5; i++ {
		resp, _ := postPair(t, server, body, "application/json")
		if i < 4 && resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
		if i == 4 && resp.StatusCode != 429 {
			t.Fatalf("attempt 5: %d", resp.StatusCode)
		}
	}
	correct := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
	resp, _ := postPair(t, server, correct, "application/json")
	if resp.StatusCode != 429 {
		t.Fatalf("blocked code accepted: %d", resp.StatusCode)
	}
	invalid := []struct{ body, ctype string }{
		{correct, "text/plain"},
		{`{"controller_id":"` + controllerA + `","extra":"field"}`, "application/json"},
		{correct + " " + correct, "application/json"},
	}
	for _, tt := range invalid {
		resp, _ = postPair(t, server, tt.body, tt.ctype)
		if resp.StatusCode != 400 {
			t.Fatalf("invalid JSON accepted: %d", resp.StatusCode)
		}
	}
}

func TestPairedControllerReadOnlyAuthorizationCheck(t *testing.T) {
	server, store, state := startPairedServer(t)
	code, _, err := store.Generate()
	if err != nil { t.Fatal(err) }
	body := `{"controller_id":"` + controllerA + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
	resp, data := postPair(t, server, body, "application/json")
	if resp.StatusCode != http.StatusCreated { t.Fatalf("pairing failed: %v", data) }
	token := data["access_token"].(string)
	check := func(controller, bearer string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/pairings/current", nil)
		if err != nil { t.Fatal(err) }
		req.Header.Set("X-EHA-Controller-ID", controller)
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("Authorization", "Bearer "+bearer)
		res, err := server.Client().Do(req)
		if err != nil { t.Fatal(err) }
		defer res.Body.Close()
		var response map[string]any
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil { t.Fatal(err) }
		return res.StatusCode, response
	}
	if status, data := check(controllerA,token); status != 200 ||
		data["authorized"] != true || data["agent_id"] != state.AgentID ||
		data["controller_id"] != controllerA {
		t.Fatalf("valid controller check failed: %d %v", status, data)
	}
	if status,_:=check(controllerB,token);status!=401 {t.Fatalf("wrong controller: %d",status)}
	if status,_:=check(controllerA,strings.Repeat("0",64));status!=401 {t.Fatalf("wrong token: %d",status)}
	if err:=store.Revoke(controllerA,state.AgentID,token);err!=nil {t.Fatal(err)}
	if status,_:=check(controllerA,token);status!=401 {t.Fatalf("revoked token accepted: %d",status)}
}
