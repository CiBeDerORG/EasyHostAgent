package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

func TestEnabledSettingsPersist(t *testing.T) {
	engine, err := dockerengine.New("/var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	dir := t.TempDir()
	s, err := New(dir, engine)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Types()) != 1 || s.Types()[0].Enabled {
		t.Fatal("new Driver should be disabled")
	}
	if err := s.Set("easytier", true); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(filepath.Join(dir, "adapter-settings.json"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("settings mode: %v %v", stat, err)
	}
	s2, err := New(dir, engine)
	if err != nil || !s2.Types()[0].Enabled {
		t.Fatalf("persistence failed: %v", err)
	}
	if err := s2.Set("unexpected", true); err == nil {
		t.Fatal("unknown Driver enabled")
	}
	if err := s2.Set("easytier", false); err != nil {
		t.Fatal(err)
	}
	s3, err := New(dir, engine)
	if err != nil || s3.Types()[0].Enabled {
		t.Fatalf("disabled state not persisted: %v", err)
	}
}
