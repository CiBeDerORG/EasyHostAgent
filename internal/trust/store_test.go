package trust

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

const (
	testAgent    = "11111111-1111-4111-8111-111111111111"
	testSource   = "22222222-2222-4222-8222-222222222222"
	testApp      = "sub2api"
	testRevision = "0123456789abcdef0123456789abcdef01234567"
)

func makeCandidateZIP(t *testing.T, mutate func(*bundles.CandidateInfo)) []byte {
	t.Helper()
	manifest := []byte("schema_version: 1\nid: sub2api\n")
	script := []byte("#!/bin/sh\nexit 0\n")
	msha := sha256.Sum256(manifest)
	ssha := sha256.Sum256(script)
	meta := bundles.CandidateInfo{
		FormatVersion: 1, SourceID: testSource, ApplicationID: testApp,
		GitRevision: testRevision, ManifestSHA256: hex.EncodeToString(msha[:]),
		Files: []bundles.CandidateFile{{
			Source: "sub2api/deploy/install.sh", Destination: "sub2api/deploy/install.sh",
			Mode: "0755", SizeBytes: int64(len(script)), SHA256: hex.EncodeToString(ssha[:]),
		}},
	}
	if mutate != nil {
		mutate(&meta)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{"bundle-manifest.json", raw, 0644},
		{"sub2api/app.yaml", manifest, 0644},
		{"sub2api/deploy/install.sh", script, 0755},
	} {
		header := &zip.FileHeader{Name: file.name, Method: zip.Store}
		header.SetMode(file.mode)
		stream, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Write(file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTrustStore(t *testing.T, agentID string) (*Store, *bundles.Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := New(dir, agentID, cache)
	if err != nil {
		t.Fatal(err)
	}
	return approvals, cache, dir
}

func putCandidate(t *testing.T, cache *bundles.Store, data []byte) string {
	t.Helper()
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if _, exists, err := cache.Put(context.Background(), digest, bytes.NewReader(data)); err != nil || exists {
		t.Fatalf("测试 ZIP 缓存失败: %v", err)
	}
	return digest
}

func TestLocalBundleApprovalRequiresExplicitVerifiedIdentity(t *testing.T) {
	trustStore, cache, dir := makeTrustStore(t, testAgent)
	digest := putCandidate(t, cache, makeCandidateZIP(t, nil))
	if _, err := trustStore.Check(digest); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("未批准就通过执行门禁: %v", err)
	}
	for _, tc := range []struct{ source, app, revision string }{
		{"different-source", testApp, testRevision},
		{testSource, "other-app", testRevision},
		{testSource, testApp, strings.Repeat("a", 40)},
	} {
		if _, err := trustStore.Approve(digest, tc.source, tc.app, tc.revision); err == nil {
			t.Fatal("错误身份获得了本机批准")
		}
		if _, err := trustStore.Check(digest); !errors.Is(err, ErrNotApproved) {
			t.Fatal("失败的批准操作意外产生记录")
		}
	}
	approved, err := trustStore.Approve(digest, testSource, testApp, testRevision)
	if err != nil || approved.AgentID != testAgent || approved.SourceID != testSource {
		t.Fatalf("可信终端批准失败: %+v %v", approved, err)
	}
	info, err := os.Stat(filepath.Join(dir, "approvals", digest+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("批准文件权限不安全: %v %v", info, err)
	}
	afterRestart, err := New(dir, testAgent, cache)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := afterRestart.Check(digest)
	if err != nil || checked.SHA256 != digest || checked.ManifestSHA256 == "" {
		t.Fatalf("重启后信任状态丢失: %+v %v", checked, err)
	}
	repeat, err := afterRestart.Approve(digest, testSource, testApp, testRevision)
	if err != nil || repeat.ApprovedAt != approved.ApprovedAt {
		t.Fatal("重复批准不应覆盖既有批准记录")
	}
	if err := afterRestart.Revoke(digest); err != nil {
		t.Fatal(err)
	}
	if _, err := afterRestart.Check(digest); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("撤销后仍被允许: %v", err)
	}
	if err := afterRestart.Revoke(digest); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("重复撤销未失败关闭: %v", err)
	}
}

func TestMalformedCandidateCannotBeApproved(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bundles.CandidateInfo)
	}{
		{"伪造 Manifest 摘要", func(m *bundles.CandidateInfo) { m.ManifestSHA256 = strings.Repeat("0", 64) }},
		{"伪造资源 SHA", func(m *bundles.CandidateInfo) { m.Files[0].SHA256 = strings.Repeat("0", 64) }},
		{"文件长度不一致", func(m *bundles.CandidateInfo) { m.Files[0].SizeBytes++ }},
		{"清单声称额外文件", func(m *bundles.CandidateInfo) { m.Files = append(m.Files, m.Files[0]) }},
		{"不安全来源映射", func(m *bundles.CandidateInfo) { m.Files[0].Source = "other/install.sh" }},
		{"非法权限", func(m *bundles.CandidateInfo) { m.Files[0].Mode = "0777" }},
		{"伪造应用 ID", func(m *bundles.CandidateInfo) { m.ApplicationID = "other" }},
		{"未固定 Git 分支", func(m *bundles.CandidateInfo) { m.GitRevision = "main" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cache, _ := makeTrustStore(t, testAgent)
			digest := putCandidate(t, cache, makeCandidateZIP(t, tc.mutate))
			if _, err := store.Approve(digest, testSource, testApp, testRevision); err == nil {
				t.Fatal("来源说明或资源摘要被篡改仍可批准")
			}
		})
	}
}

func TestCorruptApprovalOrCacheFailsClosed(t *testing.T) {
	store, cache, dir := makeTrustStore(t, testAgent)
	digest := putCandidate(t, cache, makeCandidateZIP(t, nil))
	if _, err := store.Approve(digest, testSource, testApp, testRevision); err != nil {
		t.Fatal(err)
	}
	approvalFile := filepath.Join(dir, "approvals", digest+".json")
	if err := os.WriteFile(approvalFile, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Check(digest); !errors.Is(err, ErrCorruptApproval) {
		t.Fatalf("损坏的批准文件意外通过: %v", err)
	}
	if _, err := store.Approve(digest, testSource, testApp, testRevision); !errors.Is(err, ErrCorruptApproval) {
		t.Fatalf("损坏的批准文件被自动替换: %v", err)
	}
	if err := os.Remove(approvalFile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(digest, testSource, testApp, testRevision); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(approvalFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Check(digest); !errors.Is(err, ErrCorruptApproval) {
		t.Fatalf("不安全的文件权限未被拒绝: %v", err)
	}
	if err := os.Chmod(approvalFile, 0600); err != nil {
		t.Fatal(err)
	}
	anotherAgent, err := New(dir, "33333333-3333-4333-8333-333333333333", cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anotherAgent.Check(digest); !errors.Is(err, ErrCorruptApproval) {
		t.Fatalf("不同 Agent 冒用了原批准: %v", err)
	}
	storedZIP := filepath.Join(dir, "bundles", digest+".zip")
	if err := os.WriteFile(storedZIP, []byte("modified archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Check(digest); err == nil {
		t.Fatal("缓存内容被篡改后仍被批准")
	}
}
