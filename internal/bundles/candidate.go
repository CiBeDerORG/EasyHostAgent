package bundles

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
)

var (
	appIdentifier = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	gitCommit     = regexp.MustCompile(`^[a-f0-9]{40}$`)
	agentSource   = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

type CandidateFile struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

// CandidateInfo 是不可变 ZIP 的来源说明，不是发布者签名或信任批准。
type CandidateInfo struct {
	FormatVersion  int             `json:"format_version"`
	SourceID       string          `json:"source_id"`
	ApplicationID  string          `json:"application_id"`
	GitRevision    string          `json:"git_revision"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Files          []CandidateFile `json:"files"`
}

func readCandidateEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, ErrInvalidArchive
	}
	reader, err := file.Open()
	if err != nil {
		return nil, ErrInvalidArchive
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(data) > int(limit) {
		return nil, ErrInvalidArchive
	}
	return data, nil
}

// Inspect 先重算缓存 ZIP 的 SHA-256，再逐项核对候选清单与实际文件。
// 来源字段是自报声明，只有本地操作者独立核对后才可能给予批准。
func (s *Store) Inspect(digest string) (CandidateInfo, error) {
	ok, _, err := s.Has(digest)
	if err != nil {
		return CandidateInfo{}, err
	}
	if !ok {
		return CandidateInfo{}, os.ErrNotExist
	}
	name, err := s.filename(digest)
	if err != nil {
		return CandidateInfo{}, err
	}
	fd, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return CandidateInfo{}, ErrCorruptStored
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 ||
		info.Size() <= 0 || info.Size() > MaxBundleSize {
		return CandidateInfo{}, ErrCorruptStored
	}
	// 文件名与内容属于信任材料，必须再次防止读取间隙中的内容替换。
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(fd, 0, info.Size())); err != nil ||
		hex.EncodeToString(sum.Sum(nil)) != digest {
		return CandidateInfo{}, ErrCorruptStored
	}
	reader, err := zip.NewReader(fd, info.Size())
	if err != nil {
		return CandidateInfo{}, ErrInvalidArchive
	}
	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		if !safeName(file.Name) || !file.Mode().IsRegular() || files[strings.ToLower(file.Name)] != nil {
			return CandidateInfo{}, ErrInvalidArchive
		}
		files[strings.ToLower(file.Name)] = file
	}
	metadataFile := files["bundle-manifest.json"]
	if metadataFile == nil {
		return CandidateInfo{}, errors.New("候选 ZIP 缺少受审计的 bundle-manifest.json")
	}
	raw, err := readCandidateEntry(metadataFile, 128*1024)
	if err != nil {
		return CandidateInfo{}, err
	}
	var candidate CandidateInfo
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		return CandidateInfo{}, errors.New("候选包来源说明不合法")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return CandidateInfo{}, errors.New("候选包来源说明存在多余 JSON 数据")
	}
	if candidate.FormatVersion != 1 || !agentSource.MatchString(candidate.SourceID) ||
		!appIdentifier.MatchString(candidate.ApplicationID) || len(candidate.ApplicationID) > 64 ||
		!gitCommit.MatchString(candidate.GitRevision) || !ValidDigest(candidate.ManifestSHA256) ||
		len(candidate.Files) == 0 || len(candidate.Files) > 128 ||
		len(files) != len(candidate.Files)+2 {
		return CandidateInfo{}, ErrInvalidArchive
	}
	manifestFile := files[candidate.ApplicationID+"/app.yaml"]
	if manifestFile == nil {
		return CandidateInfo{}, ErrInvalidArchive
	}
	manifest, err := readCandidateEntry(manifestFile, 128*1024)
	if err != nil {
		return CandidateInfo{}, err
	}
	manifestSHA := sha256.Sum256(manifest)
	if hex.EncodeToString(manifestSHA[:]) != candidate.ManifestSHA256 {
		return CandidateInfo{}, ErrDigestMismatch
	}
	seenSources, seenDestinations := map[string]bool{}, map[string]bool{}
	var total int64
	for _, entry := range candidate.Files {
		source, dest := strings.ToLower(entry.Source), strings.ToLower(entry.Destination)
		if !safeName(entry.Source) || !safeName(entry.Destination) ||
			seenSources[source] || seenDestinations[dest] || dest == "bundle-manifest.json" ||
			dest == strings.ToLower(candidate.ApplicationID+"/app.yaml") {
			return CandidateInfo{}, ErrInvalidArchive
		}
		seenSources[source], seenDestinations[dest] = true, true
		if strings.HasPrefix(entry.Destination, "deploy/lib/") {
			if entry.Source != strings.TrimPrefix(entry.Destination, "deploy/") {
				return CandidateInfo{}, ErrInvalidArchive
			}
		} else if entry.Source != entry.Destination ||
			!strings.HasPrefix(entry.Source, candidate.ApplicationID+"/") {
			return CandidateInfo{}, ErrInvalidArchive
		}
		if entry.Mode != "0644" && entry.Mode != "0755" {
			return CandidateInfo{}, ErrInvalidArchive
		}
		if entry.SizeBytes < 0 || entry.SizeBytes > 5<<20 || !ValidDigest(entry.SHA256) {
			return CandidateInfo{}, ErrInvalidArchive
		}
		total += entry.SizeBytes
		if total > 24<<20 {
			return CandidateInfo{}, ErrInvalidArchive
		}
		file := files[dest]
		if file == nil || file.Mode().Perm() != map[string]os.FileMode{"0644": 0644, "0755": 0755}[entry.Mode] {
			return CandidateInfo{}, ErrInvalidArchive
		}
		data, err := readCandidateEntry(file, 5<<20)
		if err != nil || int64(len(data)) != entry.SizeBytes {
			return CandidateInfo{}, ErrInvalidArchive
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != entry.SHA256 {
			return CandidateInfo{}, ErrDigestMismatch
		}
	}
	return candidate, nil
}
