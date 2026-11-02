// Package tasks 维护不可执行的部署意图、幂等键和应用级资源预留。
// prepared 仅表示门禁通过并已持久化；本包绝不启动 Shell、Docker 或后台工作器。
package tasks

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/trust"
)

var (
	ErrUnauthorized = errors.New("当前控制中心授权无效")
	ErrConflict = errors.New("部署意图与已预留应用资源冲突")
	ErrNotFound = errors.New("部署任务不存在")
	ErrCorrupt = errors.New("任务存储损坏，拒绝创建或执行")
	ErrInvalid = errors.New("部署任务请求不合法")
	taskUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	appID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

const (
	Prepared = "prepared"
	Cancelled = "cancelled"
	Superseded = "superseded"
	maxTasks = 2048
)

type Input struct {
	RequestID string `json:"request_id"`
	Action string `json:"action"`
	BundleSHA256 string `json:"bundle_sha256"`
}

type Task struct {
	ID string `json:"id"`
	AgentID string `json:"agent_id"`
	ControllerID string `json:"controller_id"`
	DeviceID string `json:"device_id"`
	GrantAt string `json:"grant_at"`
	RequestID string `json:"request_id"`
	Action string `json:"action"`
	BundleSHA256 string `json:"bundle_sha256"`
	SourceID string `json:"source_id"`
	ApplicationID string `json:"application_id"`
	GitRevision string `json:"git_revision"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Status string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ExecutionEnabled bool `json:"execution_enabled"`
}

type Store struct {
	dir string
	agentID string
	grants *pairing.Store
	trust *trust.Store
}

func New(dataDir, agentID string, grants *pairing.Store, approvals *trust.Store) (*Store, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" ||
		!taskUUID.MatchString(agentID) || grants == nil || approvals == nil {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrCorrupt
	}
	dir := filepath.Join(dataDir, "tasks")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrCorrupt
	}
	return &Store{dir: dir, agentID: agentID, grants: grants, trust: approvals}, nil
}

func (s *Store) authorize(controllerID, token string) (string, string, error) {
	device, at, valid := s.grants.ActiveGrant(controllerID, s.agentID, token)
	if !valid {
		return "", "", ErrUnauthorized
	}
	return device, at.UTC().Format(time.RFC3339Nano), nil
}

func (s *Store) locked(action func() error) error {
	lockPath := filepath.Join(s.dir, ".tasks.lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return ErrCorrupt
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer fd.Close()
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	info, err := fd.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return ErrCorrupt
	}
	return action()
}

func (s *Store) file(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) load() ([]Task, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxTasks+1 {
		return nil, ErrCorrupt
	}
	result := make([]Task, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".tasks.lock" {
			continue
		}
		if !strings.HasSuffix(name, ".json") || !taskUUID.MatchString(strings.TrimSuffix(name, ".json")) {
			return nil, ErrCorrupt
		}
		info, err := os.Lstat(filepath.Join(s.dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
			info.Size() == 0 || info.Size() > 8192 {
			return nil, ErrCorrupt
		}
		fd, err := os.OpenFile(filepath.Join(s.dir, name), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, ErrCorrupt
		}
		decoder := json.NewDecoder(io.LimitReader(fd, 8192))
		decoder.DisallowUnknownFields()
		var task Task
		decErr := decoder.Decode(&task)
		var extra any
		endErr := decoder.Decode(&extra)
		closeErr := fd.Close()
		if decErr != nil || !errors.Is(endErr, io.EOF) || closeErr != nil ||
			task.ID+".json" != name || task.AgentID != s.agentID ||
			!taskUUID.MatchString(task.ControllerID) || !taskUUID.MatchString(task.DeviceID) ||
			!taskUUID.MatchString(task.RequestID) || !bundles.ValidDigest(task.BundleSHA256) ||
			!bundles.ValidDigest(task.ManifestSHA256) || !appID.MatchString(task.ApplicationID) ||
			len(task.ApplicationID) > 64 || task.SourceID == "" ||
			len(task.GitRevision) != 40 || task.GrantAt == "" ||
			(task.Status != Prepared && task.Status != Cancelled && task.Status != Superseded) ||
			(task.Action != "install" && task.Action != "update" && task.Action != "uninstall") ||
			task.ExecutionEnabled {
			return nil, ErrCorrupt
		}
		if _, err := time.Parse(time.RFC3339Nano, task.GrantAt); err != nil {
			return nil, ErrCorrupt
		}
		if _, err := time.Parse(time.RFC3339Nano, task.CreatedAt); err != nil {
			return nil, ErrCorrupt
		}
		if _, err := time.Parse(time.RFC3339Nano, task.UpdatedAt); err != nil {
			return nil, ErrCorrupt
		}
		result = append(result, task)
	}
	return result, nil
}

func newTaskID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = buf[6]&0x0f | 0x40
	buf[8] = buf[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[:4], buf[4:6], buf[6:8], buf[8:10], buf[10:]), nil
}

// storeOne 在进程间文件锁内持久化；首次创建不可覆盖，同一任务状态修改使用原子替换。
func (s *Store) storeOne(task Task, replace bool) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".task-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := s.file(task.ID)
	if replace {
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return ErrCorrupt
		}
		if err := os.Rename(tmp.Name(), target); err != nil {
			return err
		}
	} else if err := os.Link(tmp.Name(), target); err != nil {
		return err
	}
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func sameOwner(t Task, controller, device, grantAt string) bool {
	return t.ControllerID == controller && t.DeviceID == device && t.GrantAt == grantAt
}

// Submit 只登记经过本机信任验证的部署意图；不会执行安装。
// 同一个 request_id 的不同参数必须冲突；同一个应用只能被当前授权纪元持有一个 prepared 预留。
func (s *Store) Submit(controllerID, token string, in Input) (Task, bool, error) {
	if !taskUUID.MatchString(in.RequestID) || !bundles.ValidDigest(in.BundleSHA256) ||
		(in.Action != "install" && in.Action != "update" && in.Action != "uninstall") {
		return Task{}, false, ErrInvalid
	}
	var result Task
	var repeated bool
	err := s.locked(func() error {
		device, grantAt, err := s.authorize(controllerID, token)
		if err != nil {
			return err
		}
		all, err := s.load()
		if err != nil {
			return err
		}
		approval, err := s.trust.Check(in.BundleSHA256)
		if err != nil {
			return err
		}
		// 幂等键碰撞先于资源冲突；同请求重试永不产生第二个任务。
		for _, task := range all {
			if task.RequestID == in.RequestID {
				if !sameOwner(task, controllerID, device, grantAt) ||
					task.BundleSHA256 != in.BundleSHA256 || task.Action != in.Action {
					return ErrConflict
				}
				result, repeated = task, true
				return nil
			}
		}
		for _, task := range all {
			if task.Status == Prepared && task.ApplicationID == approval.ApplicationID {
				return ErrConflict
			}
		}
		if len(all) >= maxTasks {
			return ErrConflict
		}
		id, err := newTaskID()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		task := Task{
			ID: id, AgentID: s.agentID, ControllerID: controllerID, DeviceID: device,
			GrantAt: grantAt, RequestID: in.RequestID, Action: in.Action,
			BundleSHA256: in.BundleSHA256, SourceID: approval.SourceID,
			ApplicationID: approval.ApplicationID, GitRevision: approval.GitRevision,
			ManifestSHA256: approval.ManifestSHA256, Status: Prepared,
			CreatedAt: now, UpdatedAt: now, ExecutionEnabled: false,
		}
		if err := s.storeOne(task, false); err != nil {
			return err
		}
		result = task
		return nil
	})
	return result, repeated, err
}

func (s *Store) Get(controllerID, token, id string) (Task, error) {
	if !taskUUID.MatchString(id) {
		return Task{}, ErrNotFound
	}
	var result Task
	err := s.locked(func() error {
		device, grantAt, err := s.authorize(controllerID, token)
		if err != nil {
			return err
		}
		all, err := s.load()
		if err != nil {
			return err
		}
		for _, task := range all {
			if task.ID == id {
				if !sameOwner(task, controllerID, device, grantAt) {
					return ErrNotFound
				}
				result = task
				return nil
			}
		}
		return ErrNotFound
	})
	return result, err
}

func (s *Store) List(controllerID, token string) ([]Task, error) {
	result := make([]Task, 0)
	err := s.locked(func() error {
		device, grantAt, err := s.authorize(controllerID, token)
		if err != nil {
			return err
		}
		all, err := s.load()
		if err != nil {
			return err
		}
		for _, task := range all {
			if sameOwner(task, controllerID, device, grantAt) {
				result = append(result, task)
			}
		}
		return nil
	})
	return result, err
}

// Cancel 仅允许当前有效控制中心撤销自身的 prepared 预留，不声称停止进程。
func (s *Store) Cancel(controllerID, token, id string) (Task, error) {
	if !taskUUID.MatchString(id) {
		return Task{}, ErrNotFound
	}
	var result Task
	err := s.locked(func() error {
		device, grantAt, err := s.authorize(controllerID, token)
		if err != nil {
			return err
		}
		all, err := s.load()
		if err != nil {
			return err
		}
		for _, task := range all {
			if task.ID == id {
				if !sameOwner(task, controllerID, device, grantAt) {
					return ErrNotFound
				}
				if task.Status == Prepared {
					task.Status = Cancelled
					task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
					if err := s.storeOne(task, true); err != nil {
						return err
					}
				}
				result = task
				return nil
			}
		}
		return ErrNotFound
	})
	return result, err
}
