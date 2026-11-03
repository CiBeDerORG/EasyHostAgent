package market

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

func TestStatusUsesPairingAndChecksExternalOwnership(t *testing.T) {
	ctx := context.Background()
	data := filepath.Join(t.TempDir(), "data")
	software := filepath.Join(t.TempDir(), "software")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(data)
	if err != nil {
		t.Fatal(err)
	}
	known := Ref{Source: testSource, App: "demo-reader"}
	external := Ref{Source: testSource, App: "outside-deploy"}
	scan := func(context.Context) (dockerengine.GroupedInventory, error) {
		return dockerengine.GroupedInventory{
			Complete: true,
			Resources: []dockerengine.Resource{
				{ComposeProject: external.App, Status: "running"},
			},
		}, nil
	}
	cfg := config.Config{DataDir: data, SoftwareRoot: software, Platform: "linux"}
	noGrant, err := New(ctx, cfg, "", cache, scan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := noGrant.Status(ctx, testController, []Ref{known}, false)
	if err != nil || len(rows) != 1 || !rows[0].Allowed || rows[0].BlockedReason != "" {
		t.Fatalf("已配对控制端无需额外授权应用: %+v %v", rows, err)
	}
	allowed, err := New(ctx, cfg, testController+"/"+testSource, cache, scan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = allowed.Status(ctx, testController, []Ref{known, external}, false)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	if !rows[0].Allowed || rows[0].BlockedReason != "" || rows[0].State != "not_installed" {
		t.Fatalf("有授权的未安装应用应允许安装: %+v", rows[0])
	}
	if rows[1].Allowed || !rows[1].External || rows[1].State != "running" ||
		rows[1].BlockedReason != "external_project" {
		t.Fatalf("外部运行中的项目不能被误认成受管应用: %+v", rows[1])
	}
	if err := allowed.save(Record{Ref: Ref{Source: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", App: known.App}, Managed: true}); err != nil {
		t.Fatal(err)
	}
	rows, err = allowed.Status(ctx, testController, []Ref{known}, false)
	if err != nil || len(rows) != 1 || rows[0].Allowed || rows[0].BlockedReason != "source_conflict" {
		t.Fatalf("其他来源的旧记录不得覆盖: %+v %v", rows, err)
	}
}
