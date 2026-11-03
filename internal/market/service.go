package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

type Ref struct {
	Source string `json:"source_id"`
	App    string `json:"app_id"`
}

func (r Ref) valid() bool {
	return uuid.MatchString(r.Source) && len(r.App) <= 64 && appID.MatchString(r.App)
}

type Request struct {
	Ref
	Digest          string            `json:"bundle_sha256,omitempty"`
	Config          map[string]string `json:"config,omitempty"`
	ConfirmExternal bool              `json:"confirm_external,omitempty"`
}

type Record struct {
	Ref
	Mode     string   `json:"mode,omitempty"`
	Digest   string   `json:"bundle_sha256,omitempty"`
	Action   string   `json:"action,omitempty"`
	Result   string   `json:"result,omitempty"`
	Message  string   `json:"message,omitempty"`
	Managed  bool     `json:"managed"`
	Services []string `json:"services,omitempty"`
}

type Status struct {
	Record
	State          string `json:"state"`
	Allowed        bool   `json:"allowed"`
	External       bool   `json:"external,omitempty"`
	BlockedReason  string `json:"blocked_reason,omitempty"`
	Adoptable      bool   `json:"adoptable,omitempty"`
	ForceUpdatable bool   `json:"force_updatable,omitempty"`
}

var ErrDenied = errors.New("控制端配对授权无效")
var ErrBusy = errors.New("主机正在执行应用操作，请稍后刷新")
var ErrConflict = errors.New("应用不属于本来源或主机上已存在同名项目")

type inventory func(context.Context) (dockerengine.GroupedInventory, error)

type Service struct {
	ctx           context.Context
	root, records string
	platform      string
	cache         *bundles.Store
	scan          inventory
	mu            sync.Mutex
	active        string
	jobs          sync.WaitGroup
}

func New(ctx context.Context, cfg config.Config, grants string, cache *bundles.Store, scan inventory) (*Service, error) {
	s := &Service{ctx: ctx, records: filepath.Join(cfg.DataDir, "app-records"), root: filepath.Join(cfg.SoftwareRoot, ".easyhostagent-apps"), platform: cfg.Platform, cache: cache, scan: scan}
	if err := privateDir(s.records); err != nil {
		return nil, err
	}
	// 旧版应用级授权参数仅用于兼容已有配置；管理权限由 HTTP 配对身份校验控制。
	_ = grants
	return s, nil
}

// 应用无需逐一授权，但控制端必须具备有效 UUID；动作执行时还会核对配对令牌。
func (s *Service) hasGrant(controller string, ref Ref) bool {
	return uuid.MatchString(controller) && ref.valid()
}

func (s *Service) read(id string) (Record, error) {
	var rec Record
	raw, err := os.ReadFile(filepath.Join(s.records, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return rec, nil
	}
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.App != id || !rec.Ref.valid() {
		return Record{}, errors.New("应用安装记录不可读")
	}
	if rec.Result == "running" && s.active != id {
		rec.Result = "unknown"
		rec.Message = "上次操作中断，请核对服务状态"
	}
	return rec, nil
}

func (s *Service) save(r Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.records, r.App+".json"), raw, 0600)
}

// 调用前先取消服务上下文。等正在受理的请求退出临界区，再等待脚本进程组退出并写回结果。
func (s *Service) Wait() {
	s.mu.Lock()
	s.mu.Unlock()
	s.jobs.Wait()
}

func (s *Service) Status(ctx context.Context, controller string, refs []Ref, includeManaged bool) ([]Status, error) {
	if len(refs) > 256 {
		return nil, errors.New("每批最多查询 256 个应用")
	}
	for _, ref := range refs {
		if !ref.valid() {
			return nil, errors.New("应用标识无效")
		}
	}
	snap, scanErr := s.scan(ctx)
	projects := map[string]dockerengine.Resource{}
	for _, p := range snap.Resources {
		if p.ComposeProject != "" {
			projects[p.ComposeProject] = p
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if includeManaged {
		seen := map[Ref]bool{}
		for _, ref := range refs {
			seen[ref] = true
		}
		entries, err := os.ReadDir(s.records)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !appID.MatchString(id) {
				continue
			}
			rec, err := s.read(id)
			if err != nil {
				return nil, err
			}
			if rec.Managed && !seen[rec.Ref] {
				refs = append(refs, rec.Ref)
			}
		}
	}
	result := make([]Status, 0, len(refs))
	for _, ref := range refs {
		r, err := s.read(ref.App)
		if err != nil {
			return nil, err
		}
		foreign := r.App != "" && r.Source != ref.Source
		if r.Ref != ref {
			r = Record{Ref: ref}
		}
		row := Status{Record: r, Allowed: s.hasGrant(controller, ref) && !foreign, State: "unknown"}
		if !s.hasGrant(controller, ref) {
			row.BlockedReason = "grant_required"
		} else if foreign {
			row.BlockedReason = "source_conflict"
		}
		if scanErr == nil && snap.Complete {
			row.State = "not_installed"
			if p, ok := projects[ref.App]; ok {
				row.State = p.Status
				legacyMatch := s.validLegacyProject(p, ref.App)
				// Existing Agent-managed apps use private workspace labels.
				// An explicitly adopted legacy Deploy app must still match
				// its original, fixed Compose directory on every scan.
				row.External = !r.Managed || (r.Mode == legacyDeployMode || r.Mode == directDeployMode) && !legacyMatch
				if row.External {
					row.Allowed = false
					if !s.hasGrant(controller, ref) {
						row.BlockedReason = "grant_required"
					} else if foreign {
						row.BlockedReason = "source_conflict"
					} else if !r.Managed && legacyMatch {
						if _, err := s.legacyScript(ref.App, "update"); err != nil {
							row.BlockedReason = "legacy_scripts_missing"
							row.ForceUpdatable = true
						} else {
							row.Adoptable = true
							row.BlockedReason = "adoption_required"
						}
					} else if r.Mode == legacyDeployMode && !legacyMatch {
						row.BlockedReason = "legacy_identity_mismatch"
					} else {
						row.BlockedReason = "external_project"
					}
				}
				if r.Managed && row.State == "running" {
					for _, required := range r.Services {
						if !slices.Contains(p.Services, required) {
							row.State = "partial"
							break
						}
					}
				}
			} else if ref.App == "opencode" && !r.Managed {
				// 历史 OpenCode 未指定 Compose name，只有核对唯一实例完整标签后才能只读关联。
				for _, candidate := range snap.Resources {
					if s.legacyOpenCodeProject(candidate) {
						row.State = candidate.Status
						row.External = true
						row.Allowed = false
						row.BlockedReason = "legacy_project_name"
						break
					}
				}
			} else if r.Managed {
				row.State = "missing"
			}
		}
		// 已受管且身份有效的运行实例可重复执行固定来源的受控更新脚本。
		if r.Managed && !row.External && row.Allowed &&
			slices.Contains([]string{"running", "stopped", "partial"}, row.State) {
			row.ForceUpdatable = true
		}
		result = append(result, row)
	}
	return result, nil
}

func (s *Service) Start(ctx context.Context, controller, name string, req Request, authorized func() bool) (Record, error) {
	if !req.Ref.valid() || (name != "install" && name != "update" && name != "uninstall") {
		return Record{}, errors.New("应用或动作无效")
	}
	if !s.hasGrant(controller, req.Ref) || !authorized() {
		return Record{}, ErrDenied
	}
	if (name == "install" || name == "update") && !bundles.ValidDigest(req.Digest) ||
		name == "update" && len(req.Config) != 0 ||
		name == "uninstall" && (req.Digest != "" || len(req.Config) != 0) {
		return Record{}, errors.New("动作参数无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != "" {
		return Record{}, ErrBusy
	}
	if s.ctx.Err() != nil {
		return Record{}, errors.New("Agent 正在关闭")
	}
	old, err := s.read(req.App)
	if err != nil {
		return Record{}, err
	}
	if old.App != "" && old.Source != req.Source {
		return Record{}, ErrConflict
	}
	if name == "update" && !old.Managed {
		return Record{}, errors.New("应用尚未安装，请先安装")
	}
	if name == "uninstall" {
		if !old.Managed {
			if old.Action == name && old.Result == "succeeded" {
				return old, nil
			}
			return Record{}, errors.New("没有本机安装记录")
		}
		req.Digest = old.Digest
	} else if name == "install" && old.Managed {
		if old.Digest != req.Digest {
			return Record{}, errors.New("应用已安装；请使用更新操作")
		}
	}
	info, files, err := s.cache.ReadCandidate(req.Digest)
	if err != nil || info.ApplicationID != req.App || info.SourceID != req.Source {
		return Record{}, errors.New("安装包校验失败或与来源不符")
	}
	m, err := loadManifest(info, files)
	if err != nil {
		return Record{}, err
	}
	if name == "update" && m.Actions["update"].Strategy != "script" {
		return Record{}, errors.New("应用未声明受支持的更新脚本")
	}
	if name == "install" || name == "update" {
		if !slices.Contains(m.Compatibility.Platforms, s.platform) || !slices.Contains(m.Compatibility.Architectures, runtime.GOARCH) {
			return Record{}, errors.New("应用未声明支持本机系统或架构")
		}
		if m.Runtime.RequiresRoot && os.Geteuid() != 0 {
			return Record{}, errors.New("此应用要求 Agent 以 root 身份运行")
		}
	}
	if old.Mode == legacyDeployMode {
		if name == "install" && len(req.Config) != 0 {
			return Record{}, errors.New("原有 Deploy 安装使用现有配置，不允许通过商店覆盖参数")
		}
		// Original Deploy installation keeps its own paths, data and secrets.
		// Do not materialize a separate Agent package over an existing app.
		if name == "install" && old.Managed {
			return Record{}, errors.New("原有 Deploy 应用已安装，请使用更新")
		}
		if m.Actions[name].Strategy != "script" ||
			m.Actions[name].Path != "deploy/"+name+".sh" || len(m.Actions[name].Argv) != 0 {
			return Record{}, errors.New("当前应用清单不支持原有 Deploy 部署动作")
		}
		if _, err := s.legacyScript(req.App, name); err != nil {
			return Record{}, err
		}
		snap, err := s.scan(ctx)
		if err != nil || !snap.Complete {
			return Record{}, errors.New("无法完整验证原有 Deploy 应用的容器归属")
		}
		found := false
		for _, p := range snap.Resources {
			if p.ComposeProject != req.App {
				continue
			}
			if found || !s.validLegacyProject(p, req.App) {
				return Record{}, ErrConflict
			}
			found = true
		}
		if name == "install" && found || name != "install" && !found {
			return Record{}, errors.New("原有 Deploy 项目状态与操作不匹配，请刷新状态")
		}
		if !authorized() {
			return Record{}, ErrDenied
		}
		rec := Record{Ref: req.Ref, Digest: req.Digest, Action: name,
			Result: "running", Managed: true, Mode: legacyDeployMode,
			Services: m.Verification.Services}
		if err := s.save(rec); err != nil {
			return Record{}, errors.New("无法保存原有 Deploy 应用操作记录")
		}
		s.active = req.App
		s.jobs.Add(1)
		go s.run(rec, m, filepath.Join(filepath.Dir(s.root), req.App),
			s.legacyWorkdir(req.App), nil, authorized)
		return rec, nil
	}
	// 标准 Deploy 清单直接使用 <SOFTWARE_ROOT>/<应用ID>/deploy 和共享配置。
	// 非标准历史候选包继续保留原来的隔离安装路径，避免破坏已有记录。
	if old.Mode == directDeployMode || (!old.Managed && deployLayout(m)) {
		return s.startDirectDeploy(ctx, req, name, old, m, info, files, authorized)
	}
	sourceRoot := filepath.Join(s.root, req.Source)
	root := filepath.Join(sourceRoot, req.App)
	work := filepath.Join(root, req.App, filepath.Dir(m.Runtime.File))
	snapshot, err := s.scan(ctx)
	if err != nil || !snapshot.Complete {
		return Record{}, errors.New("无法确认目标 Docker 项目的归属")
	}
	for _, p := range snapshot.Resources {
		if p.ComposeProject != req.App {
			continue
		}
		if !old.Managed {
			return Record{}, ErrConflict
		}
		for _, c := range p.Containers {
			if filepath.Clean(c.Labels["com.docker.compose.project.working_dir"]) != work {
				return Record{}, ErrConflict
			}
		}
		if name == "install" && old.Action == "install" && old.Result == "succeeded" && p.Status == "running" {
			return old, nil
		}
	}
	if err = ensureSoftwareRoot(filepath.Dir(s.root)); err != nil {
		return Record{}, errors.New("软件根目录不可用或可被其他用户替换")
	}
	for _, dir := range []string{s.root, sourceRoot, root} {
		if err = privateDir(dir); err != nil {
			return Record{}, errors.New("私有应用目录不可用")
		}
	}
	if err = materialize(root, info, files); err != nil {
		return Record{}, errors.New("无法写入已校验的包文件")
	}
	// Update reuses the existing env/secrets and never overwrites user settings.
	// The update.sh from the validated Deploy application bundle is authoritative.
	values, err := prepareConfig(root, m.Config, req.Config, name == "install")
	if err != nil {
		return Record{}, err
	}
	if !authorized() {
		return Record{}, ErrDenied
	}
	rec := Record{Ref: req.Ref, Digest: req.Digest, Action: name, Result: "running", Managed: true, Services: m.Verification.Services}
	if err = s.save(rec); err != nil {
		return Record{}, errors.New("无法保存安装记录")
	}
	s.active = req.App
	s.jobs.Add(1)
	go s.run(rec, m, root, work, values, authorized)
	return rec, nil
}

func (s *Service) run(rec Record, m manifest, root, work string, values map[string]string, authorized func() bool) {
	defer s.jobs.Done()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()
	a := m.Actions[rec.Action]
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + os.Getenv("HOME")}
	var cmd *exec.Cmd
	if rec.Mode == legacyDeployMode || rec.Mode == directDeployMode {
		// Execute only the fixed, locally approved original Deploy script.
		// The UI never provides paths, script bodies or arbitrary args.
		path, err := s.legacyScript(rec.App, rec.Action)
		if err != nil {
			s.finishLegacyFailure(rec, err)
			return
		}
		cmd = exec.CommandContext(ctx, "/bin/bash", path)
	} else if a.Strategy == "compose_down" {
		// 兼容旧 v1 清单；默认 down 保留数据卷，固定项目、目录与 Compose 文件。
		cmd = exec.CommandContext(ctx, "docker", "compose", "-p", rec.App, "-f", filepath.Join(root, rec.App, m.Runtime.File), "down")
		for k, v := range values {
			env = append(env, k+"="+v)
		}
	} else {
		args := append([]string{filepath.Join(root, rec.App, a.Path)}, a.Argv...)
		cmd = exec.CommandContext(ctx, "/bin/bash", args...)
	}
	cmd.Dir = work
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// 脚本和容器输出可能含初始化凭据；只返回固定原因及退出码。
	var err error
	if authorized() {
		err = cmd.Run()
	} else {
		err = ErrDenied
	}
	rec.Result = "succeeded"
	if err != nil {
		rec.Result = "failed"
		rec.Message = "执行失败，请检查主机的运行环境"
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			rec.Message = fmt.Sprintf("脚本退出码 %d", exit.ExitCode())
		}
		if ctx.Err() != nil {
			rec.Message = "操作超时或 Agent 已停止"
		}
		if errors.Is(err, ErrDenied) {
			rec.Message = ErrDenied.Error()
		}
	} else if rec.Action == "uninstall" {
		rec.Managed = false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = ""
	if err := s.save(rec); err != nil {
		// 保留磁盘上的中断标识，下次查询显示未知，避免谎报成功。
		return
	}
}

func (s *Service) finishLegacyFailure(rec Record, _ error) {
	rec.Result = "failed"
	rec.Message = "原有 Deploy 脚本不可用或权限不安全"
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = ""
	_ = s.save(rec)
}
