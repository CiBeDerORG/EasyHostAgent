// Package app owns the native HTTPS server lifecycle without heartbeat,
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
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/httpapi"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	state, err := identity.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("HTTPS listen %q: %w", cfg.ListenAddr, err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{state.Certificate},
	}
	srv := &http.Server{
		Handler:           httpapi.NewHandler(state, cfg.Platform, buildinfo.Version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(tls.NewListener(ln, tlsConfig)) }()
	logger.Info("agent_start", "agent_id", state.AgentID, "listen", ln.Addr().String(),
		"tls_spki_sha256", state.SPKISHA256, "platform", cfg.Platform)
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
