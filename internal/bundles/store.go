// Package bundles stores immutable, inspected ZIP v1 application bundles.
// Uploading is strictly data handling; no bundle extraction or execution occurs.
package bundles

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	MaxBundleSize   int64  = 64 << 20
	maxExpandedSize uint64 = 256 << 20
	maxFiles               = 1024
)

var (
	ErrInvalidDigest  = errors.New("invalid SHA-256 digest")
	ErrDigestMismatch = errors.New("bundle digest mismatch")
	ErrTooLarge       = errors.New("bundle exceeds 64 MiB")
	ErrInvalidArchive = errors.New("unsupported or unsafe ZIP bundle")
	ErrCorruptStored  = errors.New("existing bundle is not valid; manual recovery required")
)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var filePartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Store struct {
	dir string
}

func New(dataDir string) (*Store, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" {
		return nil, errors.New("bundle data directory must be private and absolute")
	}
	parent, err := os.Lstat(dataDir)
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("bundle data directory is not private")
	}
	dir := filepath.Join(dataDir, "bundles")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("bundle cache is not a private real directory")
	}
	return &Store{dir: dir}, nil
}
func ValidDigest(digest string) bool {
	return digestPattern.MatchString(digest)
}
func (s *Store) filename(digest string) (string, error) {
	if !ValidDigest(digest) {
		return "", ErrInvalidDigest
	}
	return filepath.Join(s.dir, digest+".zip"), nil
}
func (s *Store) verifyStored(digest string) (bool, int64, error) {
	target, err := s.filename(digest)
	if err != nil {
		return false, 0, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > MaxBundleSize {
		return false, 0, ErrCorruptStored
	}
	fd, err := os.OpenFile(target, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, 0, ErrCorruptStored
	}
	defer fd.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(fd, MaxBundleSize+1))
	if err != nil || n != info.Size() || hex.EncodeToString(h.Sum(nil)) != digest {
		return false, 0, ErrCorruptStored
	}
	return true, n, nil
}

// Has never treats corrupted, substituted or symlinked cached content as present.
func (s *Store) Has(digest string) (bool, int64, error) {
	return s.verifyStored(digest)
}

func safeName(value string) bool {
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\\\x00:\r\n") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || !filePartPattern.MatchString(part) ||
			strings.HasPrefix(part, ".") || part == "data" || part == "runtime" ||
			part == ".env" || part == ".secrets" {
			return false
		}
	}
	return true
}

// ValidateZIP inspects filenames, file types, sizes and CRC through bounded
// decompression, and never extracts any file onto the host filesystem.
func ValidateZIP(path string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%w: invalid archive", ErrInvalidArchive)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxFiles {
		return ErrInvalidArchive
	}
	seen := make(map[string]bool, len(reader.File))
	var total uint64
	var regular int
	for _, f := range reader.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !safeName(name) || seen[strings.ToLower(name)] {
			return ErrInvalidArchive
		}
		seen[strings.ToLower(name)] = true
		mode := f.Mode()
		if strings.HasSuffix(f.Name, "/") {
			if !mode.IsDir() {
				return ErrInvalidArchive
			}
			continue
		}
		if !mode.IsRegular() || (f.Flags&0x1) != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return ErrInvalidArchive
		}
		regular++
		if f.UncompressedSize64 > uint64(MaxBundleSize) ||
			total > maxExpandedSize-f.UncompressedSize64 {
			return ErrInvalidArchive
		}
		total += f.UncompressedSize64
		stream, err := f.Open()
		if err != nil {
			return ErrInvalidArchive
		}
		n, readErr := io.CopyN(io.Discard, stream, int64(f.UncompressedSize64)+1)
		closeErr := stream.Close()
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			return ErrInvalidArchive
		}
	}
	if regular == 0 {
		return ErrInvalidArchive
	}
	return nil
}

// Put writes a bounded temporary file, checks the exact-byte digest and ZIP
// safety, then atomically hard-links it into the immutable cache. EEXIST
// never overwrites an existing file (even if corrupted).
func (s *Store) Put(ctx context.Context, digest string, body io.Reader) (int64, bool, error) {
	target, err := s.filename(digest)
	if err != nil {
		return 0, false, err
	}
	// 已有缓存也不能跳过本次 PUT 请求体的摘要与归档安全校验。
	exists, _, err := s.verifyStored(digest)
	if err != nil {
		return 0, false, err
	}
	temp, err := os.CreateTemp(s.dir, ".upload-*.tmp")
	if err != nil {
		return 0, false, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(0600); err != nil {
		return 0, false, err
	}
	hash := sha256.New()
	n, copyErr := io.CopyN(io.MultiWriter(temp, hash), body, MaxBundleSize+1)
	if n > MaxBundleSize {
		return 0, false, ErrTooLarge
	}
	var tooBig *http.MaxBytesError
	if errors.As(copyErr, &tooBig) {
		return 0, false, ErrTooLarge
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return 0, false, copyErr
	}
	if n == 0 || hex.EncodeToString(hash.Sum(nil)) != digest {
		return 0, false, ErrDigestMismatch
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if err := temp.Sync(); err != nil {
		return 0, false, err
	}
	if err := ValidateZIP(temp.Name()); err != nil {
		return 0, false, err
	}
	if err := temp.Close(); err != nil {
		return 0, false, err
	}
	if exists {
		// 验证本次字节后再确认原有缓存依然完整，避免并发替换或丢失造成假成功。
		ok, storedSize, verifyErr := s.verifyStored(digest)
		if verifyErr != nil {
			return 0, false, verifyErr
		}
		if !ok {
			return 0, false, ErrCorruptStored
		}
		return storedSize, true, nil
	}
	if err := os.Link(temp.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			ok, existingSize, verifyErr := s.verifyStored(digest)
			if verifyErr != nil {
				return 0, false, verifyErr
			}
			if ok {
				return existingSize, true, nil
			}
		}
		return 0, false, err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return n, false, nil
}
