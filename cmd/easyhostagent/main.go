// EasyHostAgent - a native HTTPS host management agent for EasyTier.
//
// T01-04 adds local-only pairing codes and a durable controller grant.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/CiBeDerORG/EasyHostAgent/internal/app"
	"github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("easyhostagent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print build version")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	showIdentity := flags.Bool("identity", false, "show stable Agent ID and TLS public key fingerprint locally")
	pairingCode := flags.Bool("pairing-code", false, "generate a single-use pairing code from a trusted host terminal")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments:", flags.Args())
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "configuration error:", err)
		return 1
	}
	if *checkConfig {
		fmt.Fprintf(stdout, "configuration valid: data_dir=%s software_root=%s listen=%s log_level=%s\n",
			cfg.DataDir, cfg.SoftwareRoot, cfg.ListenAddr, cfg.LogLevel)
		return 0
	}
	if *showIdentity && *pairingCode {
		fmt.Fprintln(stderr, "choose only one of -identity or -pairing-code")
		return 2
	}
	if *showIdentity || *pairingCode {
		state, err := identity.LoadOrCreate(cfg.DataDir)
		if err != nil {
			fmt.Fprintln(stderr, "identity error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Agent ID: %s\nTLS SPKI SHA-256: %s\n", state.AgentID, state.SPKISHA256)
		if *pairingCode {
			store, err := pairing.New(cfg.DataDir, state.AgentID)
			if err != nil {
				fmt.Fprintln(stderr, "pairing store error:", err)
				return 1
			}
			code, expires, err := store.Generate()
			if err != nil {
				fmt.Fprintln(stderr, "pairing error:", err)
				return 1
			}
			fmt.Fprintf(stdout, "One-time pairing code: %s\nExpires (UTC): %s\n", code, expires.Format("2006-01-02T15:04:05Z"))
		}
		return 0
	}

	logger := logging.New(stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("agent_failed", "error", err)
		return 1
	}
	return 0
}
