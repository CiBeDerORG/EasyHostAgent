package reverse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestPreparePairingStoresBootstrapPrivatelyAndReplacesOldGrant(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("a", 64)
	if err := store.InstallGrant("22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", old); err != nil {
		t.Fatal(err)
	}
	newToken := strings.Repeat("b", 64)
	if err := PreparePairing(dir, "https://control.example.com", newToken, false, store); err == nil {
		t.Fatal("re-pair without explicit confirmation was accepted")
	}
	if err := PreparePairing(dir, "https://control.example.com", newToken, true, store); err != nil {
		t.Fatal(err)
	}
	present, err := PairingStatus(dir, store)
	if err != nil || !present {
		t.Fatalf("pair status=%v err=%v", present, err)
	}
	if store.Authorized("22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", old) {
		t.Fatal("old grant survived explicit re-pair")
	}
	info, err := os.Stat(filepath.Join(dir, bootstrapFileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap permissions: %v %v", info, err)
	}
	c := &client{bootstrap: filepath.Join(dir, bootstrapFileName)}
	got, err := c.loadBootstrap()
	if err != nil || got != newToken {
		t.Fatalf("bootstrap=%q err=%v", got, err)
	}
	if err := c.clearBootstrap(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparePairingRequiresControllerAnd64HexToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := pairing.New(dir, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePairing(dir, "", strings.Repeat("a", 64), false, store); err == nil {
		t.Fatal("missing controller URL accepted")
	}
	if err := PreparePairing(dir, "https://control.example.com", "not-a-token", false, store); err == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestReverseControllerHTTPAndHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  bool
	}{
		{name: "http", tls: false},
		{name: "https", tls: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "test-token"
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agent/discovery" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, "bad path", http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("expected Bearer token")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if gotTLS := r.TLS != nil; gotTLS != tc.tls {
					t.Errorf("unexpected TLS state: %v", gotTLS)
				}
				if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
					t.Errorf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
				}
				var body struct {
					AgentID string `json:"agent_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AgentID != "test-agent" {
					t.Errorf("unexpected body: %+v, %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"pending","next_poll_seconds":10}`))
			})
			var server *httptest.Server
			if tc.tls {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()

			c := &client{
				cfg:  config.Config{ControllerURL: server.URL},
				http: server.Client(),
			}
			var reply discoveryReply
			status, err := c.post(context.Background(), "/api/v1/agent/discovery", token,
				map[string]string{"agent_id": "test-agent"}, &reply)
			if err != nil || status != http.StatusOK || reply.Status != "pending" || reply.NextPoll != 10 {
				t.Fatalf("POST over %s: status=%d reply=%+v error=%v", tc.name, status, reply, err)
			}
		})
	}
}
