// Package deploypackage validates and durably stages *data-only* Deploy bundles.
// Passing this check never authorizes a shell command or Docker operation.
package deploypackage

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	MaxArchiveBytes  = 26 << 20
	MaxFileBytes     = 5 << 20
	MaxManifestBytes = 128 << 10
	MaxMetadataBytes = 256 << 10
	MaxFiles         = 128
)

var (
	ErrInvalid   = errors.New("invalid deploy package")
	ErrTooLarge  = errors.New("deploy package size limit exceeded")
	appPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	sha40Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha64Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type File struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Mode        string `json:"mode"`
}
type Envelope struct {
	FormatVersion  int               `json:"format_version"`
	AppID          string            `json:"app_id"`
	GitRevision    string            `json:"git_revision"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	ManifestPath   string            `json:"manifest_path"`
	AllowedActions map[string]string `json:"allowed_actions"`
	Files          []File            `json:"files"`
}
type Verified struct {
	Envelope      Envelope
	ArchiveSHA256 string
	ArchiveSize   int64
	FileCount     int
}
type observed struct {
	sha  string
	size int64
	mode string
}

func validID(id string) bool { return len(id) >= 2 && len(id) <= 64 && appPattern.MatchString(id) }
func safePath(name string) bool {
	if name == "" || len(name) > 256 || strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, "\\\x00\r\n") || path.Clean(name) != name {
		return false
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") {
			return false
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
				!(c >= '0' && c <= '9') && c != '-' && c != '_' && c != '.' {
				return false
			}
		}
		lower := strings.ToLower(p)
		switch lower {
		case "data", "state", "backup", "runtime", "workspace", ".env", ".secrets", ".git", ".ssh", "id_rsa", "id_ed25519":
			return false
		}
	}
	return true
}
func parseEnvelope(raw []byte) (Envelope, error) {
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: bundle metadata", ErrInvalid)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Envelope{}, fmt.Errorf("%w: trailing bundle metadata", ErrInvalid)
	}
	e := envelope
	if e.FormatVersion != 1 || !validID(e.AppID) || !sha40Pattern.MatchString(e.GitRevision) ||
		!sha64Pattern.MatchString(e.ManifestSHA256) || e.ManifestPath != "manifest/app.yaml" ||
		len(e.Files) == 0 || len(e.Files) > MaxFiles {
		return Envelope{}, fmt.Errorf("%w: bundle fields", ErrInvalid)
	}
	if len(e.AllowedActions) == 0 || len(e.AllowedActions) > 3 || e.AllowedActions["install"] != "script" {
		return Envelope{}, fmt.Errorf("%w: unsupported action contract", ErrInvalid)
	}
	for name, strategy := range e.AllowedActions {
		if (name != "install" && name != "update" && name != "uninstall") ||
			((name == "install" || name == "update") && strategy != "script") ||
			(name == "uninstall" && strategy != "compose_down") {
			return Envelope{}, fmt.Errorf("%w: unsupported action strategy", ErrInvalid)
		}
	}
	seenSrc := map[string]bool{}
	seenDst := map[string]bool{}
	total := int64(0)
	containsInstall, containsUpdate := false, false
	for _, file := range e.Files {
		if !safePath(file.Source) || !safePath(file.Destination) ||
			!sha64Pattern.MatchString(file.SHA256) || file.Size < 0 || file.Size > MaxFileBytes ||
			(file.Mode != "0644" && file.Mode != "0755") ||
			seenSrc[strings.ToLower(file.Source)] || seenDst[strings.ToLower(file.Destination)] {
			return Envelope{}, fmt.Errorf("%w: invalid file entry", ErrInvalid)
		}
		seenSrc[strings.ToLower(file.Source)] = true
		seenDst[strings.ToLower(file.Destination)] = true
		if strings.HasPrefix(file.Source, "lib/") {
			if file.Destination != "deploy/"+file.Source {
				return Envelope{}, fmt.Errorf("%w: shared library remap", ErrInvalid)
			}
		} else if file.Source != file.Destination || !strings.HasPrefix(file.Source, e.AppID+"/") {
			return Envelope{}, fmt.Errorf("%w: foreign package file", ErrInvalid)
		}
		total += file.Size
		if total > 24<<20 {
			return Envelope{}, ErrTooLarge
		}
		if file.Destination == e.AppID+"/deploy/install.sh" && file.Mode == "0755" {
			containsInstall = true
		}
		if file.Destination == e.AppID+"/deploy/update.sh" && file.Mode == "0755" {
			containsUpdate = true
		}
	}
	if !containsInstall || (e.AllowedActions["update"] == "script" && !containsUpdate) {
		return Envelope{}, fmt.Errorf("%w: required action script missing", ErrInvalid)
	}
	if !seenDst[e.AppID+"/deploy/docker-compose.yaml"] {
		return Envelope{}, fmt.Errorf("%w: missing Compose resource", ErrInvalid)
	}
	return envelope, nil
}

// Verify reads a tar payload under strict per-entry bounds and cross-checks
// every byte against the immutable manifest and file inventory.
func Verify(reader io.Reader, expectedDigest string) (Verified, error) {
	if !sha64Pattern.MatchString(expectedDigest) {
		return Verified{}, fmt.Errorf("%w: expected SHA-256", ErrInvalid)
	}
	// LimitReader prevents a malicious tar from causing unbounded consumption.
	limited := &io.LimitedReader{R: reader, N: MaxArchiveBytes + 1}
	var stream bytes.Buffer
	if _, err := io.Copy(&stream, limited); err != nil {
		return Verified{}, err
	}
	if stream.Len() > MaxArchiveBytes {
		return Verified{}, ErrTooLarge
	}
	whole := sha256.Sum256(stream.Bytes())
	digest := hex.EncodeToString(whole[:])
	if digest != expectedDigest {
		return Verified{}, fmt.Errorf("%w: archive SHA-256 mismatch", ErrInvalid)
	}
	t := tar.NewReader(bytes.NewReader(stream.Bytes()))
	entries := map[string]observed{}
	lowerNames := map[string]bool{}
	var bundle, manifest []byte
	for {
		hdr, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Verified{}, fmt.Errorf("%w: unreadable tar", ErrInvalid)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return Verified{}, fmt.Errorf("%w: links, directories and special entries forbidden", ErrInvalid)
		}
		if hdr.Name != "bundle.json" && hdr.Name != "manifest/app.yaml" && !safePath(hdr.Name) {
			return Verified{}, fmt.Errorf("%w: unsafe archive entry", ErrInvalid)
		}
		key := strings.ToLower(hdr.Name)
		if lowerNames[key] || len(entries) >= MaxFiles+2 {
			return Verified{}, fmt.Errorf("%w: duplicate or excess archive entries", ErrInvalid)
		}
		lowerNames[key] = true
		max := int64(MaxFileBytes)
		if hdr.Name == "bundle.json" {
			max = MaxMetadataBytes
		}
		if hdr.Name == "manifest/app.yaml" {
			max = MaxManifestBytes
		}
		if hdr.Size < 0 || hdr.Size > max {
			return Verified{}, ErrTooLarge
		}
		value := make([]byte, hdr.Size)
		if _, err := io.ReadFull(t, value); err != nil {
			return Verified{}, fmt.Errorf("%w: truncated file", ErrInvalid)
		}
		sum := sha256.Sum256(value)
		entryMode := fmt.Sprintf("%04o", hdr.Mode&0777)
		entries[hdr.Name] = observed{hex.EncodeToString(sum[:]), hdr.Size, entryMode}
		switch hdr.Name {
		case "bundle.json":
			bundle = value
		case "manifest/app.yaml":
			manifest = value
		}
	}
	if len(bundle) == 0 || len(manifest) == 0 {
		return Verified{}, fmt.Errorf("%w: bundle or manifest missing", ErrInvalid)
	}
	envelope, err := parseEnvelope(bundle)
	if err != nil {
		return Verified{}, err
	}
	m := sha256.Sum256(manifest)
	if hex.EncodeToString(m[:]) != envelope.ManifestSHA256 || entries["manifest/app.yaml"].mode != "0644" {
		return Verified{}, fmt.Errorf("%w: manifest digest or mode mismatch", ErrInvalid)
	}
	if entries["bundle.json"].mode != "0644" || len(entries) != len(envelope.Files)+2 {
		return Verified{}, fmt.Errorf("%w: unexplained archive entries", ErrInvalid)
	}
	for _, file := range envelope.Files {
		actual, ok := entries[file.Destination]
		if !ok || actual.sha != file.SHA256 || actual.size != file.Size || actual.mode != file.Mode {
			return Verified{}, fmt.Errorf("%w: package file digest, size or mode mismatch", ErrInvalid)
		}
	}
	return Verified{envelope, digest, int64(stream.Len()), len(envelope.Files)}, nil
}
