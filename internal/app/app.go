// Package app owns the native HTTP/HTTPS server lifecycle without heartbeat,
// Docker polling or external task execution.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo"
	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/host"
	"github.com/CiBeDerORG/EasyHostAgent/internal/httpapi"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	state, err := identity.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}
	store, err := pairing.New(cfg.DataDir, state.AgentID)
	if err != nil {
		return fmt.Errorf("load controller authorization: %w", err)
	}
	if cfg.SoftwareRoot == "" {
		cfg.SoftwareRoot = config.DefaultLinuxRoot
		if cfg.Platform == "fnos" {
			cfg.SoftwareRoot = config.DefaultFnOSRoot
		}
	}
	hostService, err := host.New(cfg.DataDir, cfg.SoftwareRoot, cfg.Platform, state.AgentID)
	if err != nil {
		return fmt.Errorf("load host settings: %w", err)
	}
	// 第一阶段的 macOS Agent 仅开放身份、配对与主机概况。
	// 不构造 Docker/应用执行器，防止在未经适配的平台上误执行 Linux 脚本。
	var engine *dockerengine.Client
	var bundleStore *bundles.Store
	var apps *market.Service
	if cfg.Platform != "macos" {
		engine, err = dockerengine.New("")
		if err != nil {
			return fmt.Errorf("initialize local Docker Engine: %w", err)
		}
		defer engine.Close()
		bundleStore, err = bundles.New(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("initialize immutable bundle cache: %w", err)
		}
		jobsCtx, stopJobs := context.WithCancel(ctx)
		apps, err = market.New(jobsCtx, cfg, os.Getenv("EASYHOSTAGENT_APP_GRANTS"), bundleStore, engine.InspectGrouped)
		if err != nil {
			stopJobs()
			return fmt.Errorf("初始化应用执行器: %w", err)
		}
		defer func() { stopJobs(); apps.Wait() }()
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("%s listen %q: %w", cfg.Transport, cfg.ListenAddr, err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{state.Certificate},
	}
	srv := &http.Server{
		Handler:           httpapi.NewHandlerWithMarketTransport(state, cfg.Platform, buildinfo.Version, hostService, engine, bundleStore, apps, cfg.Transport == "http", store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		if cfg.Transport == "http" {
			errCh <- srv.Serve(ln)
		} else {
			errCh <- srv.Serve(tls.NewListener(ln, tlsConfig))
		}
	}()
	logger.Info("agent_start", "agent_id", state.AgentID, "listen", ln.Addr().String(),
		"tls_spki_sha256", state.SPKISHA256, "platform", cfg.Platform, "transport", cfg.Transport)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(stopCtx)
		if err != nil {
			_ = srv.Close()
		}
		<-errCh
		logger.Info("agent_stop", "reason", "signal_or_context_cancelled")
		return err
	}
}
