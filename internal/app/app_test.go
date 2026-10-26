package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

func TestRunStopsOnCancelledContext(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, config.Config{DataDir: "/var/lib/easyhostagent",
		SoftwareRoot: "/opt/software"}, logging.New(&buf, 0)); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"agent_start", "agent_stop"} {
		if !strings.Contains(buf.String(), event) {
			t.Fatalf("missing log event %s: %s", event, buf.String())
		}
	}
}
