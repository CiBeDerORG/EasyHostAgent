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

	"github.com/CiBeDerORG/EasyHostAgent/internal/adapters"
	"github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo"
	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockertasks"
	"github.com/CiBeDerORG/EasyHostAgent/internal/host"
	"github.com/CiBeDerORG/EasyHostAgent/internal/httpapi"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/reverse"
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
	// macOS 使用当前登录用户的 Docker Desktop Socket，未启动时也不阻止 Agent 运行。
	// 所有平台复用现有应用市场服务；兼容性仍由清单及执行器校验。
	engine, err := dockerengine.NewForPlatform(cfg.Platform)
	if err != nil {
		return fmt.Errorf("初始化本机 Docker Engine: %w", err)
	}
	defer engine.Close()
	// Driver 开关保存在 Agent 自己的私有设置文件，具体查询仍按请求实时执行。
	adapterService, err := adapters.New(cfg.DataDir, engine)
	if err != nil {
		return fmt.Errorf("初始化 Adapter Driver: %w", err)
	}
	// 先记录并校验持久任务，macOS 才启用受限的容器生命周期操作。
	var dockerTasks *dockertasks.Service
	if cfg.Platform == "macos" {
		dockerTasks, err = dockertasks.New(cfg.DataDir, state.AgentID, engine)
		if err != nil {
			return fmt.Errorf("初始化 Docker 持久任务: %w", err)
		}
		defer dockerTasks.Close()
	}
	var bundleStore *bundles.Store
	var apps *market.Service
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
	reverse.Start(jobsCtx, cfg, state, buildinfo.Version, apps, bundleStore, logger)
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("%s listen %q: %w", cfg.Transport, cfg.ListenAddr, err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{state.Certificate},
	}
	srv := &http.Server{
		Handler:           httpapi.NewHandlerWithAdaptersTransport(state, cfg.Platform, buildinfo.Version, hostService, engine, bundleStore, apps, dockerTasks, adapterService, cfg.Transport == "http", store),
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
