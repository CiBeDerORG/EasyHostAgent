package reverse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
)

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
