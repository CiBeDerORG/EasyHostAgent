// Package config provides environment-based host configuration.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

const MaxListenAddresses = 32

const (
	DefaultDataDir   = "/var/lib/easyhostagent"
	DefaultLinuxRoot = "/opt/software"
	DefaultFnOSRoot  = "/vol3/1000/software"
	DefaultMacOSRoot = "/opt/software"
	DefaultListen    = "0.0.0.0:7014"
	EnvTransport     = "EASYHOSTAGENT_TRANSPORT"
	EnvPlatform      = "EASYHOSTAGENT_PLATFORM"
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
	ListenAddrs  []string
	Transport    string
	Platform     string
	LogLevel     slog.Level
}

func Load() (Config, error) {
	if runtime.GOOS == "darwin" {
		if requested := os.Getenv(EnvPlatform); requested != "" && requested != "macos" {
			return Config{}, fmt.Errorf("macOS 原生进程必须使用 EASYHOSTAGENT_PLATFORM=macos")
		}
		return FromSources(func(key string) string {
			if key == EnvPlatform {
				return "macos"
			}
			return os.Getenv(key)
		}, "", false)
	}
	release, _ := os.ReadFile("/etc/os-release")
	_, markerError := os.Stat("/etc/fnos-release")
	// fnOS commonly reports Debian in /etc/os-release. Its native version
	// marker provides an additional host-only detection path.
	return FromSources(os.Getenv, string(release), markerError == nil || fnOSVersionMarker("/usr/trim/etc/version"))
}

// The host's fnOS version marker must be a non-empty regular file. Do not
// follow a symlink or trust a group/world-writable file for platform detection.
func fnOSVersionMarker(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0 && st.Mode().Perm()&0022 == 0
}

func FromSources(getenv func(string) string, osRelease string, fnosMarker bool) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("environment getter is required")
	}
	isFn := fnosMarker || isFnOS(osRelease)
	isMac := false
	// 容器内 /etc/os-release 是镜像系统，平台必须由宿主机部署配置明确告知。
	switch getenv(EnvPlatform) {
	case "":
	case "linux":
		isFn = false
	case "fnos":
		isFn = true
	case "macos":
		isFn = false
		isMac = true
	default:
		return Config{}, fmt.Errorf("EASYHOSTAGENT_PLATFORM 只能为 linux、fnos 或 macos")
	}
	root := getenv(EnvSoftwareRoot)
	if root == "" {
		root = DefaultLinuxRoot
		if isFn {
			root = DefaultFnOSRoot
		} else if isMac {
			root = DefaultMacOSRoot
		}
	}
	dataDir := getenv(EnvDataDir)
	if dataDir == "" {
		dataDir = DefaultDataDir
		if isMac {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return Config{}, fmt.Errorf("无法确定 macOS 用户目录")
			}
			dataDir = filepath.Join(home, "Library", "Application Support", "EasyHostAgent")
		}
	}
	transport := getenv(EnvTransport)
	if transport == "" {
		transport = "https"
	}
	if transport != "https" && transport != "http" {
		return Config{}, fmt.Errorf("EASYHOSTAGENT_TRANSPORT 只能为 http 或 https")
	}
	listenAddr := getenv(EnvListen)
	if listenAddr == "" {
		listenAddr = DefaultListen
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
	listenAddrs, err := ParseListenAddresses(listenAddr)
	if err != nil {
		return Config{}, err
	}
	level, err := logging.ParseLevel(getenv(EnvLogLevel))
	if err != nil {
		return Config{}, err
	}
	platform := "linux"
	if isFn {
		platform = "fnos"
	} else if isMac {
		platform = "macos"
	}
	return Config{DataDir: cleanData, SoftwareRoot: cleanRoot, ListenAddr: listenAddr, ListenAddrs: listenAddrs, Transport: transport, Platform: platform, LogLevel: level}, nil
}


// ParseListenAddresses accepts one or more comma-separated TCP bind addresses.
// A subnet is not a bind address: only locally bindable host:port entries are
// supported. Keep the existing single-address environment variable compatible.
func ParseListenAddresses(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	if len(parts) > MaxListenAddresses {
		return nil, fmt.Errorf("too many listen addresses (maximum %d)", MaxListenAddresses)
	}
	type endpoint struct {
		host string
		port int
	}
	endpoints := make([]endpoint, 0, len(parts))
	addresses := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		addr := strings.TrimSpace(part)
		if addr == "" || strings.ContainsAny(addr, " /\t\n\r") {
			return nil, fmt.Errorf("invalid listen address %q", part)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid listen address %q: %w", addr, err)
		}
		num, err := strconv.Atoi(port)
		if err != nil || num < 1 || num > 65535 {
			return nil, fmt.Errorf("invalid listen port in %q", addr)
		}
		normalized := net.JoinHostPort(host, strconv.Itoa(num))
		if seen[normalized] {
			return nil, fmt.Errorf("duplicate listen address %q", addr)
		}
		for _, previous := range endpoints {
			if previous.port != num {
				continue
			}
			// Wildcard and concrete listeners for the same address family
			// can overlap. Reject them before any sockets are opened.
			if host == "" || previous.host == "" || host == "::" || previous.host == "::" ||
				(host == "0.0.0.0" && !strings.Contains(previous.host, ":")) ||
				(previous.host == "0.0.0.0" && !strings.Contains(host, ":")) {
				return nil, fmt.Errorf("overlapping listen addresses on port %d", num)
			}
		}
		seen[normalized] = true
		endpoints = append(endpoints, endpoint{host: host, port: num})
		addresses = append(addresses, addr)
	}
	return addresses, nil
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
