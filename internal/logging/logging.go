// Package logging configures predictable, machine-readable logging.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: use debug, info, warn or error", s)
	}
}

// New writes JSON lines to the chosen writer without spawning workers.
func New(output io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: level,
	}))
}
