package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
)

func TestMacOSMarketplaceRoutesAvailableWithoutDockerDesktop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(ctx, config.Config{
			DataDir:      filepath.Join(t.TempDir(), "state"),
			SoftwareRoot: filepath.Join(t.TempDir(), "software"),
			ListenAddr:   addr,
			Platform:     "macos",
			Transport:    "https",
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-stopped:
			if err != nil {
				t.Errorf("Agent shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Agent did not stop")
		}
	}()

	// Test-only self-signed agent TLS identity, without external certificate roots.
	client := &http.Client{
		Timeout: 300 * time.Millisecond,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	base := "https://" + addr
	var identity struct {
		Platform string `json:"platform"`
		Versions []int  `json:"app_manifest_versions"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(base + "/v1/identity")
		if err == nil {
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				t.Fatalf("identity status: %d", response.StatusCode)
			}
			err = json.NewDecoder(response.Body).Decode(&identity)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case err := <-stopped:
			t.Fatalf("Agent stopped before accepting HTTPS: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("Agent HTTPS endpoint not ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if identity.Platform != "macos" || len(identity.Versions) != 2 ||
		identity.Versions[0] != 1 || identity.Versions[1] != 2 {
		t.Fatalf("Mac app marketplace must advertise existing manifest schema: %+v", identity)
	}

	request, err := http.NewRequest(http.MethodPost, base+"/v1/apps/status", bytes.NewBufferString(`{"apps":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(fmt.Errorf("call app marketplace: %w", err))
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Mac app market must exist and require pairing; got HTTP %d", response.StatusCode)
	}
}
