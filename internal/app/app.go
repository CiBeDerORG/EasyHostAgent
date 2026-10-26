// Package app owns the minimal service lifecycle. It deliberately exposes no
// network ports until authenticated HTTPS endpoints are added in T01-03.
package app

import (
	"context"
	"log/slog"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
)

// Run blocks until shutdown. It does not run polling, heartbeat, Docker, or
// installer tasks. Those belong to explicitly scoped later milestones.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("agent_start",
		"data_dir", cfg.DataDir,
		"software_root", cfg.SoftwareRoot,
	)
	<-ctx.Done()
	logger.Info("agent_stop", "reason", "signal_or_context_cancelled")
	return nil
}
