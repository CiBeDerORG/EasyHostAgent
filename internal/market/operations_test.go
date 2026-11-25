package market

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

const trackedUpdateID = "AT-aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"

func TestTrackedUpdateSurvivesForceUpdateAndRestart(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(tmp, "agent"), SoftwareRoot: filepath.Join(tmp, "software"), Platform: "linux"}
	if err := os.Mkdir(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	open := func() *Service {
		t.Helper()
		svc, err := New(ctx, cfg, "", cache, func(context.Context) (dockerengine.GroupedInventory, error) {
			return dockerengine.GroupedInventory{Complete: true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	s := open()
	ref := Ref{Source: testSource, App: "demo"}
	req := Request{Ref: ref, Digest: cacheFixture(t, cache, ref.App), OperationID: trackedUpdateID}
	if err := s.save(Record{Ref: ref, Managed: true, Digest: req.Digest, Action: "install", Result: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	authorized := func() bool { return true }
	ack, err := s.Start(ctx, testController, "update", req, authorized)
	if err != nil || ack.OperationID != trackedUpdateID || ack.Result != "running" {
		t.Fatalf("普通更新受理回执：%+v，%v", ack, err)
	}
	// 测试包的脚本检查 online 文件。首次更新缺失此文件，强制更新前再创建。
	s.Wait()
	online := filepath.Join(s.root, ref.Source, ref.App, ref.App, "online")
	if err := os.WriteFile(online, nil, 0600); err != nil {
		t.Fatal(err)
	}
	force := Request{Ref: ref, Digest: req.Digest, ConfirmExternal: true}
	if _, err := s.ForceUpdate(ctx, testController, force, authorized); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	s = open()
	latest, err := s.Status(ctx, testController, []Ref{ref}, false)
	if err != nil || len(latest) != 1 || latest[0].Result != "succeeded" || latest[0].OperationID != "" {
		t.Fatalf("无编号查询应看到强制更新的结果：%+v，%v", latest, err)
	}
	rows, err := s.OperationStatus(trackedUpdateID, []Ref{ref}, false)
	if err != nil || len(rows) != 1 || rows[0].Digest != req.Digest ||
		rows[0].OperationID != trackedUpdateID || rows[0].Result != "failed" {
		t.Fatalf("重启后查询首次更新的独立结果：%+v，%v", rows, err)
	}
	retry, err := s.Start(ctx, testController, "update", req, authorized)
	if err != nil || retry.OperationID != trackedUpdateID || retry.Result != "failed" || s.Busy() {
		t.Fatalf("同一编号应复用原结果：%+v，%v", retry, err)
	}
	req.Digest = strings.Repeat("2", 64)
	if _, err := s.Start(ctx, testController, "update", req, authorized); !errors.Is(err, ErrConflict) {
		t.Fatalf("同一编号对应不同摘要：%v", err)
	}
	for _, other := range []Ref{{Source: testController, App: ref.App}, {Source: ref.Source, App: "other"}} {
		if rows, err := s.OperationStatus(trackedUpdateID, []Ref{other}, false); err != nil || len(rows) != 0 {
			t.Fatalf("不同来源或应用不应返回回执：%+v，%v", rows, err)
		}
	}
	for _, action := range []string{"install", "uninstall"} {
		if _, err := s.Start(ctx, testController, action, req, authorized); err == nil {
			t.Fatalf("%s 不应接受更新任务编号", action)
		}
	}
	force.OperationID = trackedUpdateID
	if _, err := s.ForceUpdate(ctx, testController, force, authorized); err == nil {
		t.Fatal("强制更新不应接受普通更新的任务编号")
	}
}

func TestTrackedRunningIsUnknownAfterRestart(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{DataDir: t.TempDir(), Platform: "linux"}
	s, err := New(ctx, cfg, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{Ref: Ref{Source: testSource, App: "demo"}, OperationID: trackedUpdateID,
		Action: "update", Digest: strings.Repeat("1", 64), Result: "running"}
	if err := s.save(rec); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, cfg, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 重启后同名应用可能又在执行另一笔操作。
	restarted.active, restarted.activeOperation = rec.App, "AT-bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	rows, err := restarted.OperationStatus(trackedUpdateID, []Ref{rec.Ref}, false)
	if err != nil || len(rows) != 1 || rows[0].Result != "unknown" {
		t.Fatalf("重启前的 running 不属于现有任务：%+v，%v", rows, err)
	}
}
