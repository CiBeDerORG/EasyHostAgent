package httpapi

import (
	"net/http"
	"testing"
)

// TestEasyNetTopoExclusiveControllerRepair 验证重新配对成功后只有新的控制端拥有授权。
func TestEasyNetTopoExclusiveControllerRepair(t *testing.T) {
	server, store, state := startPairedServer(t)

	pair := func(controllerID, code string) (int, string) {
		t.Helper()
		body := `{"controller_id":"` + controllerID + `","device_id":"` + deviceA + `","pairing_code":"` + code + `"}`
		response, data := postPair(t, server, body, "application/json")
		token, _ := data["access_token"].(string)
		return response.StatusCode, token
	}

	codeA, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	status, tokenA := pair(controllerA, codeA)
	if status != http.StatusCreated || tokenA == "" {
		t.Fatalf("首次配对失败：status=%d", status)
	}
	if !store.Authorized(controllerA, state.AgentID, tokenA) {
		t.Fatal("首次控制端授权未生效")
	}

	// 仅开启新配对窗口和错误尝试都不得撤销既有控制端。
	codeB, _, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Authorized(controllerA, state.AgentID, tokenA) {
		t.Fatal("开启重新配对窗口时旧授权意外失效")
	}
	if failedStatus, _ := pair(controllerB, "00000000000000000000000000000000"); failedStatus != http.StatusUnauthorized {
		t.Fatalf("错误配对码的状态码：%d", failedStatus)
	}
	if !store.Authorized(controllerA, state.AgentID, tokenA) {
		t.Fatal("错误配对码导致旧授权失效")
	}

	status, tokenB := pair(controllerB, codeB)
	if status != http.StatusCreated || tokenB == "" || tokenA == tokenB {
		t.Fatalf("新控制端配对失败：status=%d", status)
	}
	if store.Authorized(controllerA, state.AgentID, tokenA) {
		t.Fatal("重新配对成功后旧控制端仍有权限")
	}
	if !store.Authorized(controllerB, state.AgentID, tokenB) {
		t.Fatal("新控制端授权未生效")
	}

	// 从真实 HTTP 授权层再次确认旧令牌不能访问 Agent。
	check := func(controllerID, token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/pairings/current", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-EHA-Controller-ID", controllerID)
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if got := check(controllerA, tokenA); got != http.StatusUnauthorized {
		t.Fatalf("旧控制端仍可访问：%d", got)
	}
	if got := check(controllerB, tokenB); got != http.StatusOK {
		t.Fatalf("新控制端访问失败：%d", got)
	}
}
