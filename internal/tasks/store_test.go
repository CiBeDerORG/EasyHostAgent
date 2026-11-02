package tasks

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/trust"
)

const (
	agentID      = "11111111-1111-4111-8111-111111111111"
	controllerID = "22222222-2222-4222-8222-222222222222"
	deviceID     = "33333333-3333-4333-8333-333333333333"
	sourceID     = "44444444-4444-4444-8444-444444444444"
	gitRevision  = "0123456789abcdef0123456789abcdef01234567"
)

func candidateZIP(t *testing.T) []byte {
	t.Helper()
	manifest := []byte("schema_version: 1\nid: sub2api\n")
	asset := []byte("#!/bin/sh\nexit 0\n")
	mhash, ahash := sha256.Sum256(manifest), sha256.Sum256(asset)
	desc := bundles.CandidateInfo{
		FormatVersion: 1, SourceID: sourceID, ApplicationID: "sub2api",
		GitRevision: gitRevision, ManifestSHA256: hex.EncodeToString(mhash[:]),
		Files: []bundles.CandidateFile{{
			Source: "sub2api/deploy/install.sh", Destination: "sub2api/deploy/install.sh",
			Mode: "0755", SizeBytes: int64(len(asset)), SHA256: hex.EncodeToString(ahash[:]),
		}},
	}
	meta, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"bundle-manifest.json", meta, 0644},
		{"sub2api/app.yaml", manifest, 0644},
		{"sub2api/deploy/install.sh", asset, 0755},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		file, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type testEnvironment struct {
	dir       string
	grants    *pairing.Store
	approvals *trust.Store
	tasks     *Store
	digest    string
	token     string
}

func setup(t *testing.T) testEnvironment {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	grants, err := pairing.New(dir, agentID)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	approvalStore, err := trust.New(dir, agentID, cache)
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := New(dir, agentID, grants, approvalStore)
	if err != nil {
		t.Fatal(err)
	}
	zipData := candidateZIP(t)
	hash := sha256.Sum256(zipData)
	digest := hex.EncodeToString(hash[:])
	if _, _, err := cache.Put(context.Background(), digest, bytes.NewReader(zipData)); err != nil {
		t.Fatal(err)
	}
	code, _, err := grants.Generate()
	if err != nil {
		t.Fatal(err)
	}
	paired, err := grants.Exchange(controllerID, deviceID, code)
	if err != nil {
		t.Fatal(err)
	}
	return testEnvironment{dir: dir, grants: grants, approvals: approvalStore,
		tasks: taskStore, digest: digest, token: paired.AccessToken}
}

func input(digest string) Input {
	return Input{RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Action: "install", BundleSHA256: digest}
}

func TestAdmissionIsDurableAndIdempotentWithoutExecution(t *testing.T) {
	env := setup(t)
	req := input(env.digest)
	if _, _, err := env.tasks.Submit(controllerID, env.token, req); !errors.Is(err, trust.ErrNotApproved) {
		t.Fatalf("缓存已存在但本机未批准却准入: %v", err)
	}
	if _, err := env.approvals.Approve(env.digest, sourceID, "sub2api", gitRevision); err != nil {
		t.Fatal(err)
	}
	first, repeated, err := env.tasks.Submit(controllerID, env.token, req)
	if err != nil || repeated || first.Status != Prepared || first.ExecutionEnabled ||
		first.SourceID != sourceID || first.DeviceID != deviceID {
		t.Fatalf("持久准备失败: %+v %v", first, err)
	}
	second, repeated, err := env.tasks.Submit(controllerID, env.token, req)
	if err != nil || !repeated || first.ID != second.ID {
		t.Fatalf("同请求重复创建了任务: %+v %v", second, err)
	}
	modified := req
	modified.Action = "update"
	if _, _, err := env.tasks.Submit(controllerID, env.token, modified); !errors.Is(err, ErrConflict) {
		t.Fatalf("同 request_id 不同操作没有冲突: %v", err)
	}
	modified.RequestID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, _, err := env.tasks.Submit(controllerID, env.token, modified); !errors.Is(err, ErrConflict) {
		t.Fatalf("同应用资源被重复预留: %v", err)
	}
	restarted, err := New(env.dir, agentID, env.grants, env.approvals)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.Get(controllerID, env.token, first.ID)
	if err != nil || restored.ID != first.ID || restored.Status != Prepared {
		t.Fatalf("重启后无法恢复任务: %+v %v", restored, err)
	}
	closed, err := restarted.Cancel(controllerID, env.token, first.ID)
	if err != nil || closed.Status != Cancelled {
		t.Fatalf("取消任务未释放预留: %+v %v", closed, err)
	}
	again, err := restarted.Cancel(controllerID, env.token, first.ID)
	if err != nil || again.UpdatedAt != closed.UpdatedAt {
		t.Fatalf("重复取消未幂等: %+v %v", again, err)
	}
	next, replay, err := restarted.Submit(controllerID, env.token, modified)
	if err != nil || replay || next.ID == first.ID || next.Status != Prepared {
		t.Fatalf("取消后无法为同一应用创建新任务: %+v %v", next, err)
	}
	list, err := restarted.List(controllerID, env.token)
	if err != nil || len(list) != 2 {
		t.Fatalf("任务列表不完整: %d %v", len(list), err)
	}
}

func TestCurrentGrantCannotBeReplayedAfterRepair(t *testing.T) {
	env := setup(t)
	if _, err := env.approvals.Approve(env.digest, sourceID, "sub2api", gitRevision); err != nil {
		t.Fatal(err)
	}
	old, _, err := env.tasks.Submit(controllerID, env.token, input(env.digest))
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := env.grants.Generate()
	if err != nil {
		t.Fatal(err)
	}
	newController := "55555555-5555-4555-8555-555555555555"
	paired, err := env.grants.Exchange(newController, deviceID, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.Get(controllerID, env.token, old.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("旧控制中心读取了任务: %v", err)
	}
	if _, err := env.tasks.Cancel(controllerID, env.token, old.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("旧控制中心取消了任务: %v", err)
	}
	if got, err := env.tasks.List(newController, paired.AccessToken); err != nil || len(got) != 0 {
		t.Fatalf("新控制中心看到了旧控制中心任务: %d %v", len(got), err)
	}
	if _, err := env.tasks.Get(newController, paired.AccessToken, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨控制中心访问旧任务未隔离: %v", err)
	}
	if _, _, err := env.tasks.Submit(newController, paired.AccessToken,
		Input{RequestID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Action: "install", BundleSHA256: env.digest}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧预留尚未恢复，新控制中心不应绕过独占: %v", err)
	}
}

func TestConcurrentSubmissionsCannotDoubleReserveApplication(t *testing.T) {
	env := setup(t)
	if _, err := env.approvals.Approve(env.digest, sourceID, "sub2api", gitRevision); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := make(chan Task, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := input(env.digest)
			req.RequestID = fmt.Sprintf("aaaaaaa%d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", i)
			task, _, err := env.tasks.Submit(controllerID, env.token, req)
			if err == nil {
				success <- task
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("并发提交意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	close(success)
	count := 0
	for task := range success {
		if task.Status != Prepared {
			t.Fatal("成功记录状态异常")
		}
		count++
	}
	if count != 1 {
		t.Fatalf("同一应用并发成功了 %d 个准备任务，期望 1", count)
	}
}

func TestCorruptOrSymlinkTaskStateFailsClosed(t *testing.T) {
	env := setup(t)
	if _, err := env.approvals.Approve(env.digest, sourceID, "sub2api", gitRevision); err != nil {
		t.Fatal(err)
	}
	task, _, err := env.tasks.Submit(controllerID, env.token, input(env.digest))
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(env.dir, "tasks", task.ID+".json")
	if err := os.WriteFile(filename, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.List(controllerID, env.token); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("损坏任务状态被静默忽略: %v", err)
	}
	if _, _, err := env.tasks.Submit(controllerID, env.token, input(env.digest)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("损坏任务状态后继续创建任务: %v", err)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(env.dir, "pairing.json"), filename); err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.List(controllerID, env.token); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("任务符号链接被当作合法记录: %v", err)
	}
}
