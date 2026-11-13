// Package config provides environment-based host configuration.
package config

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/logging"
)

const (
	DefaultDataDir    = "/var/lib/easyhostagent"
	DefaultLinuxRoot  = "/opt/software"
	DefaultFnOSRoot   = "/vol3/1000/software"
	DefaultMacOSRoot  = "/opt/software"
	DefaultListen     = "0.0.0.0:7014"
	EnvTransport      = "EASYHOSTAGENT_TRANSPORT"
	EnvPlatform       = "EASYHOSTAGENT_PLATFORM"
	EnvDataDir        = "EASYHOSTAGENT_DATA_DIR"
	EnvSoftwareRoot   = "EASYHOSTAGENT_SOFTWARE_ROOT"
	EnvLogLevel       = "EASYHOSTAGENT_LOG_LEVEL"
	EnvListen         = "EASYHOSTAGENT_LISTEN_ADDRESS"
	EnvControllerURL  = "EASYHOSTAGENT_CONTROLLER_URL"
	EnvDiscoveryToken = "EASYHOSTAGENT_DISCOVERY_TOKEN"
)

// Config holds host settings. DiscoveryToken is sensitive and must never be logged.
type Config struct {
	DataDir        string
	SoftwareRoot   string
	ListenAddr     string
	Transport      string
	Platform       string
	LogLevel       slog.Level
	ControllerURL  string
	DiscoveryToken string
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
	if strings.ContainsAny(listenAddr, " /\t\n\r") {
		return Config{}, fmt.Errorf("invalid listen address")
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid listen address: %w", err)
	}
	num, err := strconv.Atoi(port)
	if err != nil || num < 1 || num > 65535 {
		return Config{}, fmt.Errorf("invalid listen port: %q", port)
	}
	level, err := logging.ParseLevel(getenv(EnvLogLevel))
	if err != nil {
		return Config{}, err
	}
	controllerURL := strings.TrimSpace(getenv(EnvControllerURL))
	discoveryToken := strings.TrimSpace(getenv(EnvDiscoveryToken))
	if (controllerURL == "") != (discoveryToken == "") {
		return Config{}, fmt.Errorf("EASYHOSTAGENT_CONTROLLER_URL 与 EASYHOSTAGENT_DISCOVERY_TOKEN 必须同时配置")
	}
	if controllerURL != "" {
		parsed, parseErr := url.Parse(controllerURL)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(controllerURL, "\r\n\x00") {
			return Config{}, fmt.Errorf("EASYHOSTAGENT_CONTROLLER_URL 必须是无凭据、无查询参数的 HTTP 或 HTTPS 地址")
		}
		if len(discoveryToken) != 64 {
			return Config{}, fmt.Errorf("EASYHOSTAGENT_DISCOVERY_TOKEN 必须是 64 位十六进制")
		}
		if _, decodeErr := hex.DecodeString(discoveryToken); decodeErr != nil {
			return Config{}, fmt.Errorf("EASYHOSTAGENT_DISCOVERY_TOKEN 必须是 64 位十六进制")
		}
	}
	// 反向管理不需要公网/局域网入站端口。保持端口，仅收敛到 loopback。
	if controllerURL != "" {
		listenAddr = net.JoinHostPort("127.0.0.1", port)
	}
	platform := "linux"
	if isFn {
		platform = "fnos"
	} else if isMac {
		platform = "macos"
	}
	return Config{DataDir: cleanData, SoftwareRoot: cleanRoot, ListenAddr: listenAddr, Transport: transport, Platform: platform, LogLevel: level, ControllerURL: controllerURL, DiscoveryToken: discoveryToken}, nil
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
