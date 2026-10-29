package deploypackage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrConflict = errors.New("immutable deploy package conflict")
var ErrNotFound = errors.New("stored deploy package not found")

type Receipt struct {
	AppID          string `json:"app_id"`
	GitRevision    string `json:"git_revision"`
	ManifestSHA256 string `json:"manifest_sha256"`
	PackageSHA256  string `json:"package_sha256"`
	PackageSize    int64  `json:"package_size"`
	FileCount      int    `json:"file_count"`
	StoredAt       string `json:"stored_at"`
	ReadyForAgent  bool   `json:"ready_for_agent"`
	InstallEnabled bool   `json:"install_enabled"`
	Reused         bool   `json:"reused"`
}

type Store struct {
	mu   sync.Mutex
	root string
}

func New(dataDir string) (*Store, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" {
		return nil, fmt.Errorf("%w: invalid private data directory", ErrInvalid)
	}
	root := filepath.Join(dataDir, "deploy-packages")
	// Identity.LoadOrCreate ensures the data parent is private in production.
	// Verify all path components, including existing ancestors, are real dirs.
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	for p := root; p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symlink or non-directory package store", ErrInvalid)
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: package store must be private (0700)", ErrInvalid)
	}
	return &Store{root: root}, nil
}

func (s *Store) receiptPath(appID, revision string) (string, error) {
	if !validID(appID) || !sha40Pattern.MatchString(revision) {
		return "", fmt.Errorf("%w: invalid package key", ErrInvalid)
	}
	return filepath.Join(s.root, appID+"-"+revision+".json"), nil
}

func (s *Store) Lookup(appID, revision string) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookup(appID, revision)
}

func (s *Store) lookup(appID, revision string) (Receipt, error) {
	file, err := s.receiptPath(appID, revision)
	if err != nil {
		return Receipt{}, err
	}
	st, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return Receipt{}, ErrConflict
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return Receipt{}, ErrConflict
	}
	if receipt.AppID != appID || receipt.GitRevision != revision ||
		!sha64Pattern.MatchString(receipt.PackageSHA256) || receipt.PackageSize <= 0 ||
		receipt.PackageSize > MaxArchiveBytes || receipt.ReadyForAgent || receipt.InstallEnabled {
		return Receipt{}, ErrConflict
	}
	tarFile := filepath.Join(s.root, receipt.PackageSHA256+".tar")
	info, err := os.Lstat(tarFile)
	if err != nil {
		return Receipt{}, ErrConflict
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0077 != 0 || info.Size() != receipt.PackageSize {
		return Receipt{}, ErrConflict
	}
	archive, err := os.Open(tarFile)
	if err != nil {
		return Receipt{}, ErrConflict
	}
	defer archive.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(archive, MaxArchiveBytes+1)); err != nil {
		return Receipt{}, ErrConflict
	}
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h.Sum(nil))), []byte(receipt.PackageSHA256)) != 1 {
		return Receipt{}, ErrConflict
	}
	return receipt, nil
}

// Put validates the complete tar before publishing any artifact. Repeating an
// identical package is idempotent. A different package for the same app+commit
// is a hard conflict and NEVER changes previous package bytes or receipt.
func (s *Store) Put(ctx context.Context, reader io.Reader, expectedSHA256 string) (Receipt, error) {
	if !sha64Pattern.MatchString(expectedSHA256) {
		return Receipt{}, fmt.Errorf("%w: invalid package digest", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(s.root, ".incoming-*.tar")
	if err != nil {
		return Receipt{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return Receipt{}, err
	}
	verified, err := Verify(io.TeeReader(&cancelReader{ctx: ctx, r: reader}, tmp), expectedSHA256)
	if err != nil {
		return Receipt{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Receipt{}, err
	}
	if err := tmp.Close(); err != nil {
		return Receipt{}, err
	}
	e := verified.Envelope
	existing, err := s.lookup(e.AppID, e.GitRevision)
	if err == nil {
		if existing.PackageSHA256 != verified.ArchiveSHA256 ||
			existing.ManifestSHA256 != e.ManifestSHA256 {
			return Receipt{}, ErrConflict
		}
		existing.Reused = true
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Receipt{}, err
	}

	archivePath := filepath.Join(s.root, verified.ArchiveSHA256+".tar")
	// Link, not Rename: an existing immutable archive cannot be overwritten.
	if err := os.Link(tmp.Name(), archivePath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Receipt{}, err
		}
		return Receipt{}, ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339)
	receipt := Receipt{
		AppID: e.AppID, GitRevision: e.GitRevision, ManifestSHA256: e.ManifestSHA256,
		PackageSHA256: verified.ArchiveSHA256, PackageSize: verified.ArchiveSize,
		FileCount: verified.FileCount, StoredAt: now,
		ReadyForAgent: false, InstallEnabled: false,
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	metaTmp, err := os.CreateTemp(s.root, ".receipt-*.tmp")
	if err != nil {
		return Receipt{}, err
	}
	defer os.Remove(metaTmp.Name())
	if err := metaTmp.Chmod(0600); err != nil {
		metaTmp.Close()
		return Receipt{}, err
	}
	if _, err := metaTmp.Write(body); err != nil {
		metaTmp.Close()
		return Receipt{}, err
	}
	if err := metaTmp.Sync(); err != nil {
		metaTmp.Close()
		return Receipt{}, err
	}
	if err := metaTmp.Close(); err != nil {
		return Receipt{}, err
	}
	metaFile, err := s.receiptPath(e.AppID, e.GitRevision)
	if err != nil {
		return Receipt{}, err
	}
	if err := os.Link(metaTmp.Name(), metaFile); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Receipt{}, ErrConflict
		}
		return Receipt{}, err
	}
	if dir, err := os.Open(s.root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return receipt, nil
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
