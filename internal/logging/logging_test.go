package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for input, want := range map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "DEBUG": slog.LevelDebug,
		"warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		level, err := ParseLevel(input)
		if err != nil || level != want {
			t.Fatalf("%q: level=%s, err=%v", input, level, err)
		}
	}
	if _, err := ParseLevel("trace"); err == nil {
		t.Fatal("expected rejection for unsupported level")
	}
}

func TestNewProducesJSONLinesAndLevelFilters(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelWarn)
	logger.Info("filtered")
	logger.Warn("notice", "component", "agent")
	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("invalid log JSON: %v (%s)", err, buf.String())
	}
	if event["msg"] != "notice" || event["component"] != "agent" || event["level"] != "WARN" {
		t.Fatalf("unexpected log JSON: %#v", event)
	}
}
