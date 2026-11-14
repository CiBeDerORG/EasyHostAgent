package config

import (
	"log/slog"
	"os"
	"path/filepath"
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
			if cfg.SoftwareRoot != tt.want || cfg.DataDir != DefaultDataDir || cfg.LogLevel != slog.LevelInfo || cfg.ListenAddr != DefaultListen || cfg.Transport != "https" {
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
		{EnvListen: ":0"}, {EnvListen: "127.0.0.1:xyz"}, {EnvListen: "127.0.0.1:65536"}, {EnvListen: "0.0.0.0:7014/path"}, {EnvTransport: "tcp"}, {EnvTransport: "HTTP"}, {EnvPlatform: "win"},
	}
	for _, values := range tests {
		if _, err := FromSources(env(values), "", false); err == nil {
			t.Errorf("expected validation error for %#v", values)
		}
	}
}
func TestParseListenAddresses(t *testing.T) {
	got, err := ParseListenAddresses("192.168.1.10:7018,10.10.10.5:7019")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "192.168.1.10:7018" || got[1] != "10.10.10.5:7019" {
		t.Fatalf("unexpected addresses: %v", got)
	}
	cfg, err := FromSources(env(map[string]string{EnvListen: "127.0.0.1:7018,127.0.0.1:7019"}), "ID=ubuntu", false)
	if err != nil || len(cfg.ListenAddrs) != 2 {
		t.Fatalf("config should keep both addresses: %+v, %v", cfg, err)
	}
	for _, input := range []string{
		"127.0.0.1:7018,", ",127.0.0.1:7018",
		"127.0.0.1:7018,,127.0.0.2:7018",
		"127.0.0.1:7018,127.0.0.1:7018",
		"0.0.0.0:7018,127.0.0.1:7018",
		"192.168.1.0/24:7018", "127.0.0.1:0",
		"127.0.0.1:7018,127.0.0.2:65536",
	} {
		if _, err := ParseListenAddresses(input); err == nil {
			t.Errorf("expected rejection for %q", input)
		}
	}
	if got, err := ParseListenAddresses("127.0.0.1:7018,127.0.0.2:7018"); err != nil || len(got) != 2 {
		t.Fatalf("same port on separate IPs must work: %v, %v", got, err)
	}
}

func TestMissingEnvGetter(t *testing.T) {
	_, err := FromSources(nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfiguredPlainHTTP(t *testing.T) {
	cfg, err := FromSources(env(map[string]string{
		EnvTransport: "http",
		EnvListen:    "0.0.0.0:7014",
	}), "ID=ubuntu", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != "http" || cfg.ListenAddr != "0.0.0.0:7014" {
		t.Fatalf("HTTP 配置没有保留: %+v", cfg)
	}
}

func TestContainerPlatformExplicitOverride(t *testing.T) {
	for _, tc := range []struct {
		name, platform, osInfo, want string
	}{
		{"fnos_host_in_alpine_container", "fnos", "ID=alpine", "fnos"},
		{"linux_host_in_fnos_container", "linux", "ID=fnos", "linux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromSources(env(map[string]string{EnvPlatform: tc.platform}), tc.osInfo, false)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Platform != tc.want {
				t.Fatalf("container host platform=%s, want=%s", cfg.Platform, tc.want)
			}
		})
	}
}

func TestFnOSVersionMarkerOnDebianNativeHost(t *testing.T) {
	version := filepath.Join(t.TempDir(), "version")
	if fnOSVersionMarker(version) {
		t.Fatal("missing version file must not identify fnOS")
	}
	if err := os.WriteFile(version, []byte("1.2.0505\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !fnOSVersionMarker(version) {
		t.Fatal("regular read-only fnOS version file must be recognized")
	}
	cfg, err := FromSources(env(map[string]string{EnvSoftwareRoot: "/vol3/1000/software"}), "ID=debian\n", fnOSVersionMarker(version))
	if err != nil || cfg.Platform != "fnos" {
		t.Fatalf("Debian-based fnOS platform not recognized: %+v %v", cfg, err)
	}
	if err := os.Chmod(version, 0666); err != nil {
		t.Fatal(err)
	}
	if fnOSVersionMarker(version) {
		t.Fatal("group/world writable version file must not be trusted")
	}
	if err := os.Chmod(version, 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "version-link")
	if err := os.Symlink(version, link); err != nil {
		t.Fatal(err)
	}
	if fnOSVersionMarker(link) {
		t.Fatal("symlink version marker must not be trusted")
	}
}
