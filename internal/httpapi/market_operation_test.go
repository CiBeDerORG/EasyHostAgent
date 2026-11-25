package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestMarketConflictCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{market.ErrBusy, "app_busy"}, {market.ErrConflict, "app_conflict"}} {
		status, code := marketActionError(tc.err)
		if status != http.StatusConflict || code != tc.code {
			t.Fatalf("%v 映射为 %d %s", tc.err, status, code)
		}
	}
}

func TestMarketOperationStatusRequiresOneTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := grant.Generate()
	if err != nil {
		t.Fatal(err)
	}
	paired, err := grant.Exchange(controllerA, deviceA, code)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := market.New(context.Background(), config.Config{DataDir: dir}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithMarket(state, "linux", "dev", nil, nil, nil, apps, grant)
	const id = "AT-aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	ref := market.Ref{Source: controllerB, App: "demo"}
	for _, tc := range []struct {
		id             string
		refs           []market.Ref
		includeManaged bool
		want           int
	}{
		{id, []market.Ref{ref}, false, http.StatusOK},
		{"AT-invalid", []market.Ref{ref}, false, http.StatusBadRequest},
		{id, nil, false, http.StatusBadRequest},
		{id, []market.Ref{ref, ref}, false, http.StatusBadRequest},
		{id, []market.Ref{ref}, true, http.StatusBadRequest},
	} {
		body, err := json.Marshal(map[string]any{"apps": tc.refs, "operation_id": tc.id, "include_managed": tc.includeManaged})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://localhost/v1/apps/status", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+paired.AccessToken)
		req.Header.Set("X-EHA-Controller-ID", controllerA)
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s：状态 %d，预期 %d", body, w.Code, tc.want)
		}
		if tc.want == http.StatusOK {
			var response struct {
				Apps []market.Record `json:"apps"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Apps == nil || len(response.Apps) != 0 {
				t.Fatalf("不存在的任务应返回空数组：%s", w.Body.String())
			}
		}
	}
}
