package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/tasks"
	"github.com/CiBeDerORG/EasyHostAgent/internal/trust"
)

func taskRouteFixture(t *testing.T) (identity.State, *pairing.Store, *tasks.Store, string, string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := trust.New(dir, state.AgentID, cache)
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := tasks.New(dir, state.AgentID, grants, approvals)
	if err != nil {
		t.Fatal(err)
	}
	source := "44444444-4444-4444-8444-444444444444"
	revision := "0123456789abcdef0123456789abcdef01234567"
	manifest := []byte("schema_version: 1\nid: sub2api\n")
	script := []byte("#!/bin/sh\nexit 0\n")
	manifestSHA, scriptSHA := sha256.Sum256(manifest), sha256.Sum256(script)
	desc := bundles.CandidateInfo{
		FormatVersion: 1, SourceID: source, ApplicationID: "sub2api", GitRevision: revision,
		ManifestSHA256: hex.EncodeToString(manifestSHA[:]),
		Files: []bundles.CandidateFile{{
			Source: "sub2api/deploy/install.sh", Destination: "sub2api/deploy/install.sh",
			Mode: "0755", SizeBytes: int64(len(script)), SHA256: hex.EncodeToString(scriptSHA[:]),
		}},
	}
	meta, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"bundle-manifest.json", meta, 0644},
		{"sub2api/app.yaml", manifest, 0644},
		{"sub2api/deploy/install.sh", script, 0755},
	} {
		header := &zip.FileHeader{Name: file.name, Method: zip.Store}
		header.SetMode(file.mode)
		stream, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(buf.Bytes())
	digest := hex.EncodeToString(hash[:])
	if _, _, err := cache.Put(context.Background(), digest, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Approve(digest, source, "sub2api", revision); err != nil {
		t.Fatal(err)
	}
	controller := "22222222-2222-4222-8222-222222222222"
	device := "33333333-3333-4333-8333-333333333333"
	code, _, err := grants.Generate()
	if err != nil {
		t.Fatal(err)
	}
	paired, err := grants.Exchange(controller, device, code)
	if err != nil {
		t.Fatal(err)
	}
	return state, grants, taskStore, controller, paired.AccessToken, digest
}

func TestHTTPSPreparedTaskLifecycleAndAuth(t *testing.T) {
	state, grants, taskStore, controller, token, digest := taskRouteFixture(t)
	server := httptest.NewUnstartedServer(NewHandlerWithTaskAdmission(
		state, "linux", "dev", nil, nil, nil, taskStore, grants))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	call := func(method, route, body string, authorized bool) (*http.Response, map[string]any) {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+route, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if authorized {
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-EHA-Agent-ID", state.AgentID)
			request.Header.Set("X-EHA-Controller-ID", controller)
		}
		res, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
			res.Body.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		return res, response
	}
	body := `{"request_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","action":"install","bundle_sha256":"` + digest + `"}`
	res, _ := call("POST", "/v1/tasks", body, false)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatal("未配对控制中心提交了任务")
	}
	res, created := call("POST", "/v1/tasks", body, true)
	if res.StatusCode != http.StatusCreated || created["execution_enabled"] != false {
		t.Fatalf("任务准备错误: %d %+v", res.StatusCode, created)
	}
	task, ok := created["task"].(map[string]any)
	if !ok || task["status"] != tasks.Prepared || task["execution_enabled"] != false {
		t.Fatalf("任务准备误标记为执行: %+v", created)
	}
	id, ok := task["id"].(string)
	if !ok || id == "" {
		t.Fatal("没有持久任务 ID")
	}
	res, replay := call("POST", "/v1/tasks", body, true)
	if res.StatusCode != http.StatusOK || replay["idempotent_replay"] != true {
		t.Fatalf("相同请求不幂等: %d %+v", res.StatusCode, replay)
	}
	changed := strings.Replace(body, "install", "update", 1)
	res, _ = call("POST", "/v1/tasks", changed, true)
	if res.StatusCode != http.StatusConflict {
		t.Fatal("同一幂等键不同操作未拒绝")
	}
	res, _ = call("GET", "/v1/tasks/"+id, "", true)
	if res.StatusCode != http.StatusOK {
		t.Fatal("已创建的任务无法查询")
	}
	res, _ = call("GET", "/v1/tasks", "", true)
	if res.StatusCode != http.StatusOK {
		t.Fatal("任务列表无法查询")
	}
	res, _ = call("POST", "/v1/tasks/"+id, "", true)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("意外开放了任务执行路由")
	}
	res, cancelled := call("DELETE", "/v1/tasks/"+id, "", true)
	if res.StatusCode != http.StatusOK || cancelled["task"].(map[string]any)["status"] != tasks.Cancelled {
		t.Fatalf("任务取消失败: %d %+v", res.StatusCode, cancelled)
	}
	res, _ = call("GET", "/v1/tasks/"+id, "", false)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatal("匿名用户读取了任务记录")
	}
}
