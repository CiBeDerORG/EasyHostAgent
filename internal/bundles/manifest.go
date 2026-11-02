package bundles

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
)

// ManifestBytes 只读取与固定包摘要绑定的 Manifest，绝不提取 ZIP 至磁盘。
// 执行准入每次都使用本地缓存的实际字节，而不是由 Controller 提交独立 Manifest。
func (s *Store) ManifestBytes(digest string) ([]byte, error) {
	candidate, err := s.Inspect(digest)
	if err != nil {
		return nil, err
	}
	name, err := s.filename(digest)
	if err != nil {
		return nil, err
	}
	fd, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrCorruptStored
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > MaxBundleSize {
		return nil, ErrCorruptStored
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(fd, 0, info.Size())); err != nil ||
		hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, ErrCorruptStored
	}
	zr, err := zip.NewReader(fd, info.Size())
	if err != nil {
		return nil, ErrInvalidArchive
	}
	var found *zip.File
	for _, item := range zr.File {
		if item.Name == candidate.ApplicationID+"/app.yaml" {
			if found != nil {
				return nil, ErrInvalidArchive
			}
			found = item
		}
	}
	if found == nil {
		return nil, ErrInvalidArchive
	}
	raw, err := readCandidateEntry(found, 128<<10)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if !errors.Is(err, nil) || hex.EncodeToString(sum[:]) != candidate.ManifestSHA256 {
		return nil, ErrDigestMismatch
	}
	return raw, nil
}
