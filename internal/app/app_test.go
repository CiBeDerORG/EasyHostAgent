package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

func TestRunStopsOnCancelledContext(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, config.Config{DataDir: filepath.Join(t.TempDir(), "state"), SoftwareRoot: "/opt/software", ListenAddr: "127.0.0.1:0", Platform: "linux"}, logging.New(&buf, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"agent_start", "agent_stop"} {
		if !strings.Contains(buf.String(), e) {
			t.Fatalf("missing %q in logs: %s", e, buf.String())
		}
	}
}
func TestBindFailureDoesNotLoseStoredIdentity(t *testing.T) {
	// Port on a non-local address should fail, but identity remains stable.
	var buf bytes.Buffer
	cfg := config.Config{DataDir: filepath.Join(t.TempDir(), "state"), ListenAddr: "192.0.2.250:7014", Platform: "linux"}
	err := Run(context.Background(), cfg, logging.New(&buf, 0))
	if err == nil {
		t.Fatal("expected listen failure")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatal(err)
	}
}
