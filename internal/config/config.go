// Package config provides environment-based host configuration.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

const (
	DefaultDataDir   = "/var/lib/easyhostagent"
	DefaultLinuxRoot = "/opt/software"
	DefaultFnOSRoot  = "/vol3/1000/software"
	DefaultListen    = "0.0.0.0:7014"
	EnvDataDir       = "EASYHOSTAGENT_DATA_DIR"
	EnvSoftwareRoot  = "EASYHOSTAGENT_SOFTWARE_ROOT"
	EnvLogLevel      = "EASYHOSTAGENT_LOG_LEVEL"
	EnvListen        = "EASYHOSTAGENT_LISTEN_ADDRESS"
)

// Config holds non-secret values only.
type Config struct {
	DataDir      string
	SoftwareRoot string
	ListenAddr   string
	Platform     string
	LogLevel     slog.Level
}

func Load() (Config, error) {
	release, _ := os.ReadFile("/etc/os-release")
	_, markerError := os.Stat("/etc/fnos-release")
	return FromSources(os.Getenv, string(release), markerError == nil)
}

func FromSources(getenv func(string) string, osRelease string, fnosMarker bool) (Config, error) {
	if getenv == nil { return Config{}, fmt.Errorf("environment getter is required") }
	isFn := fnosMarker || isFnOS(osRelease)
	root := getenv(EnvSoftwareRoot)
	if root == "" {
		root = DefaultLinuxRoot
		if isFn { root = DefaultFnOSRoot }
	}
	dataDir := getenv(EnvDataDir)
	if dataDir == "" { dataDir = DefaultDataDir }
	listenAddr := getenv(EnvListen)
	if listenAddr == "" { listenAddr = DefaultListen }

	cleanData, err := cleanAbsolute("data dir", dataDir)
	if err != nil { return Config{}, err }
	cleanRoot, err := cleanAbsolute("software root", root)
	if err != nil { return Config{}, err }
	if cleanData == cleanRoot { return Config{}, fmt.Errorf("data dir and software root must be different") }
	if strings.ContainsAny(listenAddr, " /\t\n\r") { return Config{}, fmt.Errorf("invalid listen address") }
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil { return Config{}, fmt.Errorf("invalid listen address: %w", err) }
	num, err := strconv.Atoi(port)
	if err != nil || num < 1 || num > 65535 { return Config{}, fmt.Errorf("invalid listen port: %q", port) }
	level, err := logging.ParseLevel(getenv(EnvLogLevel))
	if err != nil { return Config{}, err }
	platform := "linux"
	if isFn { platform = "fnos" }
	return Config{DataDir: cleanData, SoftwareRoot: cleanRoot, ListenAddr: listenAddr, Platform: platform, LogLevel: level}, nil
}

func cleanAbsolute(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) { return "", fmt.Errorf("%s must be an absolute path", label) }
	clean := filepath.Clean(value)
	if clean == string(filepath.Separator) { return "", fmt.Errorf("%s must not be filesystem root", label) }
	return clean, nil
}

func isFnOS(osRelease string) bool {
	for _, line := range strings.Split(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok { continue }
		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "\"'"))
		switch key {
		case "ID":
			if value == "fnos" { return true }
		case "NAME":
			if value == "fnos" || value == "fnos linux" { return true }
		case "ID_LIKE":
			for _, token := range strings.Fields(value) { if token == "fnos" { return true } }
		}
	}
	return false
}
