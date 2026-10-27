package config

import (
	"log/slog"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
func TestFromSourcesDefaults(t *testing.T) {
	tests := []struct {
		name, release string
		marker        bool
		want          string
	}{
		{"linux", "ID=ubuntu\nNAME=Ubuntu\n", false, DefaultLinuxRoot},
		{"fnos_id", "ID=fnos\n", false, DefaultFnOSRoot},
		{"fnos_name", "NAME=fnOS\n", false, DefaultFnOSRoot},
		{"fnos_like", "ID=foo\nID_LIKE=\"debian fnos\"\n", false, DefaultFnOSRoot},
		{"fnos_marker", "ID=debian\n", true, DefaultFnOSRoot},
		{"not_fnos_substring", "ID=notfnos\n", false, DefaultLinuxRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := FromSources(env(nil), tt.release, tt.marker)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SoftwareRoot != tt.want || cfg.DataDir != DefaultDataDir || cfg.LogLevel != slog.LevelInfo || cfg.ListenAddr != DefaultListen {
				t.Fatalf("cfg=%+v, want root=%s", cfg, tt.want)
			}
		})
	}
}
func TestFromSourcesOverridesAndNormalization(t *testing.T) {
	cfg, err := FromSources(env(map[string]string{
		EnvSoftwareRoot: "/mnt/apps/../apps",
		EnvDataDir:      "/var/lib/myagent/",
		EnvLogLevel:     "DEBUG",
		EnvListen:       "127.0.0.1:8014",
	}), "ID=fnos", true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SoftwareRoot != "/mnt/apps" || cfg.DataDir != "/var/lib/myagent" || cfg.LogLevel != slog.LevelDebug || cfg.ListenAddr != "127.0.0.1:8014" || cfg.Platform != "fnos" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
func TestFromSourcesRejectsUnsafeSettings(t *testing.T) {
	tests := []map[string]string{
		{EnvDataDir: "relative"}, {EnvDataDir: "/"}, {EnvSoftwareRoot: "relative"}, {EnvSoftwareRoot: "/"},
		{EnvDataDir: "/opt/software", EnvSoftwareRoot: "/opt/software"}, {EnvLogLevel: "verbose"},
		{EnvListen: ":0"}, {EnvListen: "127.0.0.1:xyz"}, {EnvListen: "127.0.0.1:65536"}, {EnvListen: "0.0.0.0:7014/path"},
	}
	for _, values := range tests {
		if _, err := FromSources(env(values), "", false); err == nil {
			t.Errorf("expected validation error for %#v", values)
		}
	}
}
func TestMissingEnvGetter(t *testing.T) {
	_, err := FromSources(nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("unexpected error: %v", err)
	}
}
