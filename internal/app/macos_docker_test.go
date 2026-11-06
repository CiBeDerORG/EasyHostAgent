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

func TestMacOSAgentStartsWithoutDockerDesktop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var log bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, config.Config{
		DataDir: filepath.Join(t.TempDir(), "state"),
		SoftwareRoot: filepath.Join(t.TempDir(), "apps"),
		ListenAddr: "127.0.0.1:0",
		Platform: "macos",
		Transport: "https",
	}, logging.New(&log, 0))
	if err != nil || !strings.Contains(log.String(), "agent_start") {
		t.Fatalf("Docker Desktop 未启动时 Agent 仍应正常运行：%v，日志 %s", err, log.String())
	}
}
