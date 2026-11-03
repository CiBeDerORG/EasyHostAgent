package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPersistAndManagedAppGuard(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rootA := filepath.Join(base, "apps")
	rootB := filepath.Join(base, "other")
	s, err := New(dir, rootA, "fnos", "123")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Root(); r != rootA {
		t.Fatalf("default root %q", r)
	}
	if _, err := s.SetRoot(rootB); err != nil {
		t.Fatal(err)
	}
	s2, err := New(dir, rootA, "fnos", "123")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.Root(); r != rootB {
		t.Fatalf("persisted root %q", r)
	}
	fi, err := os.Stat(filepath.Join(dir, "host-settings.json"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("stored settings permissions: %v %v", fi, err)
	}
	appDir := filepath.Join(dir, "managed-apps")
	if err := os.Mkdir(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "instance.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.SetRoot(rootA); !errors.Is(err, ErrManagedApps) {
		t.Fatalf("must block root change with managed apps: %v", err)
	}
	if r, _ := s2.Root(); r != rootB {
		t.Fatalf("root changed after conflict: %q", r)
	}
	if _, err := s2.SetRoot(rootB); err != nil {
		t.Fatalf("idempotent patch must work: %v", err)
	}
}
func TestRootSafety(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(dir)
	for _, root := range []string{"", "/", "relative", dir, filepath.Join(dir, "apps")} {
		if _, err := New(dir, root, "linux", "123"); err == nil {
			t.Errorf("accepted unsafe root %q", root)
		}
	}
	actual := filepath.Join(base, "real")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, filepath.Join(link, "apps"), "linux", "123"); err == nil {
		t.Fatal("accepted symlink root")
	}
}
// fnOS keeps Agent state in SOFTWARE_ROOT/easyhostagent-data. The application
// workspace remains separate, and managed app paths cannot contain Agent state.
func TestSoftwareRootAllowsSeparateAgentDataDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "software")
	dataDir := filepath.Join(root, "easyhostagent-data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := New(dataDir, root, "fnos", "test-agent")
	if err != nil {
		t.Fatalf("fnOS 软件根目录下的独立 Agent 数据目录应被允许：%v", err)
	}
	if got, err := s.Root(); err != nil || got != root {
		t.Fatalf("软件根目录不正确：%q %v", got, err)
	}
	if _, err := New(dataDir, base, "fnos", "test-agent"); err != nil {
		t.Fatalf("Agent 数据位于软件根目录的下级路径应被允许：%v", err)
	}
	for _, badDataDir := range []string{
		root,
		filepath.Join(root, ".easyhostagent-apps"),
		filepath.Join(root, ".easyhostagent-apps", "source-id", "app-id"),
	} {
		if _, err := New(badDataDir, root, "fnos", "test-agent"); !errors.Is(err, ErrInvalidRoot) {
			t.Fatalf("Agent 数据目录不能与应用安装工作区重叠：%q err=%v", badDataDir, err)
		}
	}
	if _, err := New(dataDir, filepath.Join(dataDir, "software"), "fnos", "test-agent"); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("软件根目录不能被包含在 Agent 数据目录内：%v", err)
	}
}

func TestSnapshotFieldsAndRoot(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "apps")
	s, err := New(dir, root, "linux", "agent-id")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.AgentID != "agent-id" || snap.Platform != "linux" || snap.SoftwareRoot != root || snap.CPU.LogicalCores < 1 || snap.ObservedAt == "" || len(snap.Disks) == 0 {
		t.Fatalf("bad snapshot %+v", snap)
	}
}
