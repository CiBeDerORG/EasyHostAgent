package market

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const macOSHomebrewMode = "macos_homebrew"

//go:embed maestro_install.sh
var maestroInstallScript string

func defaultBrewPrefix() string {
	if runtime.GOARCH == "arm64" {
		return "/opt/homebrew"
	}
	return "/usr/local"
}

func (s *Service) supportsMaestro() bool {
	if s.platform != "macos" || runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		return false
	}
	u, err := user.Current()
	return err == nil && u.HomeDir != "" && u.HomeDir != "/" && u.HomeDir != "/var/empty"
}

// NativeInstallers 声明当前 Agent 能执行的固定安装器，不依赖 Docker 可用性。
func (s *Service) NativeInstallers() []string {
	if s.supportsMaestro() {
		return []string{"maestro"}
	}
	return []string{}
}

func maestroSearchPath(prefix string) string {
	home, _ := os.UserHomeDir()
	return strings.Join([]string{filepath.Join(prefix, "bin"), "/usr/local/bin",
		filepath.Join(home, "Library", "Android", "sdk", "platform-tools"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")
}

func maestroEnvironment(prefix string) []string {
	home, _ := os.UserHomeDir()
	return []string{"HOME=" + home, "PATH=" + maestroSearchPath(prefix), "NONINTERACTIVE=1"}
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0
}

func (s *Service) maestroStatus(row Status) Status {
	row.Mode = macOSHomebrewMode
	if !row.Allowed {
		return row
	}
	block := func(code, message string) Status {
		row.State, row.Allowed, row.BlockedReason, row.Message = "unknown", false, code, message
		return row
	}
	if !s.supportsMaestro() {
		return block("native_user_required", "请使用 Mac 登录用户运行 EasyHostAgent。")
	}
	prefix := s.brewPrefix
	if !isExecutable(filepath.Join(prefix, "bin", "brew")) {
		return block("homebrew_missing", "该 Mac 尚未在标准路径安装 Homebrew。")
	}

	// 读取本机 Homebrew 收据，不在状态轮询中运行 brew 或 Maestro。
	row.State = "not_installed"
	keg, err := filepath.EvalSymlinks(filepath.Join(prefix, "opt", "maestro"))
	if errors.Is(err, os.ErrNotExist) {
		return row
	}
	cellar, cellarErr := filepath.EvalSymlinks(filepath.Join(prefix, "Cellar", "maestro"))
	if err != nil || cellarErr != nil || filepath.Dir(keg) != cellar {
		return block("native_formula_conflict", "无法核对 Maestro 的 Homebrew 安装来源。")
	}
	var receipt struct {
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	receiptPath := filepath.Join(keg, "INSTALL_RECEIPT.json")
	info, err := os.Lstat(receiptPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return block("native_formula_conflict", "无法核对 Maestro 的 Homebrew 安装收据。")
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Source.Tap != "mobile-dev-inc/tap" {
		return block("native_formula_conflict", "检测到无法核实或来自其他 tap 的 Maestro 配方。")
	}
	row.Version = filepath.Base(keg)
	cli := filepath.Join(prefix, "bin", "maestro")
	linked, linkErr := os.Stat(cli)
	entry, entryErr := os.Stat(filepath.Join(keg, "bin", "maestro"))
	if linkErr != nil || entryErr != nil || !os.SameFile(linked, entry) || !isExecutable(cli) ||
		!isExecutable(filepath.Join(prefix, "opt", "openjdk", "libexec", "openjdk.jdk", "Contents", "Home", "bin", "java")) {
		return row
	}
	if row.Action == "install" && row.Result != "succeeded" && row.Result != "" {
		return row
	}
	for _, dir := range filepath.SplitList(maestroSearchPath(prefix)) {
		if isExecutable(filepath.Join(dir, "adb")) {
			row.State = "installed"
			break
		}
	}
	return row
}

// 调用方已核验配对、来源、动作和参数，并持有主机操作锁。
func (s *Service) startMaestro(req Request, old Record, authorized func() bool) (Record, error) {
	if old.App != "" && old.Mode != macOSHomebrewMode {
		return Record{}, ErrConflict
	}
	status := s.maestroStatus(Status{Record: old, Allowed: true})
	if !status.Allowed {
		return Record{}, errors.New(status.Message)
	}
	if !authorized() {
		return Record{}, ErrDenied
	}
	rec := Record{Ref: req.Ref, Mode: macOSHomebrewMode, Action: "install", Result: "running", Managed: true}
	if err := s.save(rec); err != nil {
		return Record{}, errors.New("无法保存 Maestro 安装记录")
	}
	s.active = req.App
	s.jobs.Add(1)
	home, _ := os.UserHomeDir()
	go s.run(rec, manifest{}, "", home, nil, authorized)
	return rec, nil
}
