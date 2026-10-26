// Package config provides environment-based host configuration.
// T01-01 does not create directories or change system settings.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

const (
	DefaultDataDir   = "/var/lib/easyhostagent"
	DefaultLinuxRoot = "/opt/software"
	DefaultFnOSRoot  = "/vol3/1000/software"
	EnvDataDir       = "EASYHOSTAGENT_DATA_DIR"
	EnvSoftwareRoot  = "EASYHOSTAGENT_SOFTWARE_ROOT"
	EnvLogLevel      = "EASYHOSTAGENT_LOG_LEVEL"
)

// Config holds non-secret values only. Never log future auth credentials.
type Config struct {
	DataDir      string
	SoftwareRoot string
	LogLevel     slog.Level
}

// Load gathers environment settings. SoftwareRoot is selected conservatively
// from FnOS markers or /etc/os-release and can always be overridden.
func Load() (Config, error) {
	release, _ := os.ReadFile("/etc/os-release")
	_, markerError := os.Stat("/etc/fnos-release")
	return FromSources(os.Getenv, string(release), markerError == nil)
}

// FromSources separates host discovery from validation for deterministic tests.
func FromSources(getenv func(string) string, osRelease string, fnosMarker bool) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("environment getter is required")
	}

	root := getenv(EnvSoftwareRoot)
	if root == "" {
		root = DefaultLinuxRoot
		if fnosMarker || isFnOS(osRelease) {
			root = DefaultFnOSRoot
		}
	}
	dataDir := getenv(EnvDataDir)
	if dataDir == "" {
		dataDir = DefaultDataDir
	}

	cleanData, err := cleanAbsolute("data dir", dataDir)
	if err != nil {
		return Config{}, err
	}
	cleanRoot, err := cleanAbsolute("software root", root)
	if err != nil {
		return Config{}, err
	}
	if cleanData == cleanRoot {
		return Config{}, fmt.Errorf("data dir and software root must be different")
	}
	level, err := logging.ParseLevel(getenv(EnvLogLevel))
	if err != nil {
		return Config{}, err
	}
	return Config{DataDir: cleanData, SoftwareRoot: cleanRoot, LogLevel: level}, nil
}

func cleanAbsolute(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", label)
	}
	clean := filepath.Clean(value)
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be filesystem root", label)
	}
	return clean, nil
}

func isFnOS(osRelease string) bool {
	for _, line := range strings.Split(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "\"'"))
		switch key {
		case "ID":
			if value == "fnos" {
				return true
			}
		case "NAME":
			if value == "fnos" || value == "fnos linux" {
				return true
			}
		case "ID_LIKE":
			for _, token := range strings.Fields(value) {
				if token == "fnos" {
					return true
				}
			}
		}
	}
	return false
}
