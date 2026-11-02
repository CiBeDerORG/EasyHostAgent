// Package trust 维护只允许本机可信终端修改的应用包批准记录。
// 控制中心配对、HTTP 上传和 ZIP SHA-256 均不能自动产生此批准。
package trust

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

var (
	uuid               = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	ErrNotApproved     = errors.New("部署包尚未获得本机批准")
	ErrCorruptApproval = errors.New("本机批准记录损坏，拒绝执行")
)

type Approval struct {
	Version        int    `json:"version"`
	AgentID        string `json:"agent_id"`
	SHA256         string `json:"sha256"`
	SourceID       string `json:"source_id"`
	ApplicationID  string `json:"application_id"`
	GitRevision    string `json:"git_revision"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ApprovedAt     string `json:"approved_at"`
}

type Store struct {
	dir     string
	agentID string
	bundles *bundles.Store
}

// New 只依赖现有的 Agent 私有数据目录与缓存，不创建任何 HTTP 管理接口。
func New(dataDir, agentID string, cache *bundles.Store) (*Store, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" || !uuid.MatchString(agentID) || cache == nil {
		return nil, errors.New("本机信任存储的 Agent 身份、目录或缓存不合法")
	}
	base, err := os.Lstat(dataDir)
	if err != nil || !base.IsDir() || base.Mode().Perm()&0077 != 0 {
		return nil, errors.New("信任存储必须位于 Agent 私有状态目录")
	}
	dir := filepath.Join(dataDir, "approvals")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("信任目录类型或权限不安全")
	}
	return &Store{dir: dir, agentID: agentID, bundles: cache}, nil
}

func equalDigest(a, b string) bool {
	left, err1 := hex.DecodeString(a)
	right, err2 := hex.DecodeString(b)
	return err1 == nil && err2 == nil && len(left) == sha256.Size &&
		len(right) == sha256.Size && subtle.ConstantTimeCompare(left, right) == 1
}

func (s *Store) withLock(action func() error) error {
	p := filepath.Join(s.dir, ".approval.lock")
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return ErrCorruptApproval
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fd, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer fd.Close()
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	return action()
}

func (s *Store) file(digest string) (string, error) {
	if !bundles.ValidDigest(digest) {
		return "", bundles.ErrInvalidDigest
	}
	return filepath.Join(s.dir, digest+".json"), nil
}

func (s *Store) read(digest string) (Approval, bool, error) {
	filename, err := s.file(digest)
	if err != nil {
		return Approval{}, false, err
	}
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return Approval{}, false, nil
	}
	if err != nil {
		return Approval{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() == 0 || info.Size() > 4096 {
		return Approval{}, false, ErrCorruptApproval
	}
	fd, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Approval{}, false, ErrCorruptApproval
	}
	defer fd.Close()
	decoder := json.NewDecoder(io.LimitReader(fd, 4096))
	decoder.DisallowUnknownFields()
	var record Approval
	if err := decoder.Decode(&record); err != nil {
		return Approval{}, false, ErrCorruptApproval
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Approval{}, false, ErrCorruptApproval
	}
	if record.Version != 1 || record.AgentID != s.agentID || !equalDigest(record.SHA256, digest) ||
		!uuid.MatchString(record.SourceID) || record.ApplicationID == "" || record.GitRevision == "" ||
		!bundles.ValidDigest(record.ManifestSHA256) || record.ApprovedAt == "" {
		return Approval{}, false, ErrCorruptApproval
	}
	if _, err := time.Parse(time.RFC3339Nano, record.ApprovedAt); err != nil {
		return Approval{}, false, ErrCorruptApproval
	}
	return record, true, nil
}

func (s *Store) inspectBound(digest string) (bundles.CandidateInfo, error) {
	candidate, err := s.bundles.Inspect(digest)
	if err != nil {
		return bundles.CandidateInfo{}, fmt.Errorf("本机缓存内容未通过完整性校验: %w", err)
	}
	return candidate, nil
}

// Approve 仅供进程本机 CLI 调用，要求操作者独立输入来源、应用和 Git Commit。
// 批准只绑定当前 Agent、明确 ZIP 字节和固定 Git 身份，不自动批准更新版本。
func (s *Store) Approve(digest, expectedSource, expectedApp, expectedRevision string) (Approval, error) {
	var approved Approval
	err := s.withLock(func() error {
		candidate, err := s.inspectBound(digest)
		if err != nil {
			return err
		}
		if candidate.SourceID != expectedSource || candidate.ApplicationID != expectedApp ||
			candidate.GitRevision != expectedRevision {
			return errors.New("操作者提供的固定来源、应用或 Git Commit 与实际 ZIP 不一致")
		}
		previous, exists, err := s.read(digest)
		if err != nil {
			return err
		}
		if exists {
			if !sameCandidate(previous, candidate) {
				return ErrCorruptApproval
			}
			approved = previous
			return nil
		}
		record := Approval{
			Version: 1, AgentID: s.agentID, SHA256: digest,
			SourceID: candidate.SourceID, ApplicationID: candidate.ApplicationID,
			GitRevision: candidate.GitRevision, ManifestSHA256: candidate.ManifestSHA256,
			ApprovedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := s.persist(record); err != nil {
			return err
		}
		approved = record
		return nil
	})
	return approved, err
}

func sameCandidate(approval Approval, candidate bundles.CandidateInfo) bool {
	return approval.SourceID == candidate.SourceID &&
		approval.ApplicationID == candidate.ApplicationID &&
		approval.GitRevision == candidate.GitRevision &&
		equalDigest(approval.ManifestSHA256, candidate.ManifestSHA256)
}

func (s *Store) persist(record Approval) error {
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".approval-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	path, err := s.file(record.SHA256)
	if err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return fmt.Errorf("本机信任记录无法安全固定（不得覆盖）: %w", err)
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Check 是未来执行门禁的必要条件，而不是完整执行许可。
// 实际执行前仍须校验当前 Controller Grant、资源冲突、任务状态与安全策略。
func (s *Store) Check(digest string) (Approval, error) {
	var result Approval
	err := s.withLock(func() error {
		record, ok, err := s.read(digest)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotApproved
		}
		candidate, err := s.inspectBound(digest)
		if err != nil {
			return err
		}
		if !sameCandidate(record, candidate) {
			return ErrCorruptApproval
		}
		result = record
		return nil
	})
	return result, err
}

// Revoke 仅供本机 CLI 调用；撤销后所有后续 Check 必须拒绝。
func (s *Store) Revoke(digest string) error {
	return s.withLock(func() error {
		_, exists, err := s.read(digest)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotApproved
		}
		filename, err := s.file(digest)
		if err != nil {
			return err
		}
		if err := os.Remove(filename); err != nil {
			return err
		}
		if d, err := os.Open(s.dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
		return nil
	})
}
