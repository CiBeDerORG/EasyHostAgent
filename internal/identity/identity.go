// Package identity manages a persistent TLS identity of an EasyHostAgent installation.
// Never silently regenerate identity after an existing state file becomes unreadable.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const stateName = "identity.json"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// State contains a verified certificate and private key. NEVER serialize this object.
type State struct {
	AgentID     string
	SPKISHA256  string
	Certificate tls.Certificate
}

type stored struct {
	Version   int    `json:"version"`
	AgentID   string `json:"agent_id"`
	CertPEM   string `json:"certificate_pem"`
	KeyPEM    string `json:"private_key_pem"`
	CreatedAt string `json:"created_at"`
}

// LoadOrCreate persists the same agent_id/certificate across process restarts.
// The file is root/private-user readable only; partial/corrupt state aborts startup.
func LoadOrCreate(dataDir string) (State, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) == "/" {
		return State{}, fmt.Errorf("identity directory must be absolute and non-root")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return State{}, fmt.Errorf("create identity dir: %w", err)
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return State{}, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return State{}, fmt.Errorf("identity dir must be a private real directory (0700): %s", dataDir)
	}
	lockPath := filepath.Join(dataDir, ".identity.lock")
	if i, e := os.Lstat(lockPath); e == nil && (!i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0) {
		return State{}, errors.New("insecure identity lock file")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return State{}, e
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return State{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return State{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	path := filepath.Join(dataDir, stateName)
	if i, e := os.Lstat(path); e == nil {
		if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
			return State{}, errors.New("insecure identity file")
		}
		buf, readErr := os.ReadFile(path)
		if readErr != nil {
			return State{}, fmt.Errorf("read existing identity: %w", readErr)
		}
		return parse(buf)
	} else if !errors.Is(e, os.ErrNotExist) {
		return State{}, e
	}
	raw, err := generate()
	if err != nil {
		return State{}, err
	}
	state, err := parse(raw)
	if err != nil {
		return State{}, err
	}

	f, err := os.CreateTemp(dataDir, ".identity-*.tmp")
	if err != nil {
		return State{}, err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return State{}, err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return State{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return State{}, err
	}
	if err := f.Close(); err != nil {
		return State{}, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return State{}, err
	}
	// Ensure directory metadata also reaches disk.
	if d, e := os.Open(dataDir); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return state, nil
}

func parse(data []byte) (State, error) {
	var s stored
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("malformed identity: %w", err)
	}
	if s.Version != 1 || !uuidPattern.MatchString(s.AgentID) {
		return State{}, errors.New("invalid identity version or UUID; manual recovery required")
	}
	pair, err := tls.X509KeyPair([]byte(s.CertPEM), []byte(s.KeyPEM))
	if err != nil {
		return State{}, fmt.Errorf("identity certificate/key mismatch: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return State{}, err
	}
	if leaf.Subject.CommonName != "EasyHostAgent "+s.AgentID || !leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return State{}, errors.New("identity certificate metadata invalid")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return State{}, errors.New("identity certificate expired or not yet valid; manual renewal required")
	}
	key, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return State{}, err
	}
	digest := sha256.Sum256(key)
	pair.Leaf = leaf
	return State{AgentID: s.AgentID, SPKISHA256: hex.EncodeToString(digest[:]), Certificate: pair}, nil
}

func generate() ([]byte, error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	idBytes[6] = (idBytes[6] & 0x0f) | 0x40
	idBytes[8] = (idBytes[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", idBytes[:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:16])
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serialBytes := make([]byte, 16)
	if _, err := rand.Read(serialBytes); err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	now := time.Now().UTC()
	cert := x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serialBytes),
		Subject:               pkix.Name{CommonName: "EasyHostAgent " + id},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed trust anchor for explicit pinning; never a public CA
		DNSNames:              []string{"localhost", host},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &cert, &cert, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(stored{
		Version: 1, AgentID: id, CreatedAt: now.Format(time.RFC3339),
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}, "", "  ")
}
