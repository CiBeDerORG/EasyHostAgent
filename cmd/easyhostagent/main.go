// EasyHostAgent - lightweight native host service for EasyTier.
//
// T01-01 foundation only: no server port, pairing or Docker calls yet.
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
		fmt.Fprintf(stdout, "configuration valid: data_dir=%s software_root=%s log_level=%s\n",
			cfg.DataDir, cfg.SoftwareRoot, cfg.LogLevel)
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
