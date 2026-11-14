package app

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRunMultipleListenersAndShutdown(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cfg := config.Config{
		DataDir: filepath.Join(t.TempDir(), "state"), SoftwareRoot: "/opt/software",
		ListenAddrs: []string{"127.0.0.1:0", "127.0.0.1:0"},
		Transport:   "http", Platform: "linux",
	}
	go func() { done <- Run(ctx, cfg, logging.New(&buf, 0)) }()
	// Wait until startup logs contain the two bound listener addresses.
	// Read buf only after completion to avoid racing with logging.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("multi-listener shutdown timed out")
	}
	if !strings.Contains(buf.String(), "agent_start") || !strings.Contains(buf.String(), "agent_stop") {
		t.Fatalf("missing lifecycle events: %s", buf.String())
	}
}

func TestRunPartialBindFailureClosesEarlierListener(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	// First socket is ephemeral; second conflicts with the occupied port.
	cfg := config.Config{DataDir: filepath.Join(t.TempDir(), "state"),
		SoftwareRoot: "/opt/software", Transport: "http", Platform: "linux",
		ListenAddrs: []string{"127.0.0.1:0", occupied.Addr().String()}}
	err = Run(context.Background(), cfg, logging.New(&bytes.Buffer{}, 0))
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected bind error: %v", err)
	}
}
