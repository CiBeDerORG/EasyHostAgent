package identity

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStableAndPrivateIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentID != second.AgentID || first.SPKISHA256 != second.SPKISHA256 {
		t.Fatal("identity changed across restart")
	}
	if len(first.SPKISHA256) != 64 {
		t.Fatal("invalid spki fingerprint")
	}
	b, err := x509.MarshalPKIXPublicKey(first.Certificate.Leaf.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != first.SPKISHA256 {
		t.Fatal("SPKI mismatch")
	}
	info, err := os.Stat(filepath.Join(dir, stateName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions: %o", info.Mode().Perm())
	}
}

func TestCorruptIdentityNeverRegenerated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stateName)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("corruption accepted")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "corrupt" {
		t.Fatal("corrupt file was replaced unexpectedly")
	}
}

func TestIdentityCannotBeSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, stateName)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("symlink allowed")
	}
}
func TestIdentityDirectoryMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("public identity dir accepted")
	}
}
func TestConcurrentLoadCreatesOneIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var wg sync.WaitGroup
	ids := make([]string, 12)
	for i := range ids {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			s, e := LoadOrCreate(dir)
			if e == nil {
				ids[index] = s.AgentID
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("not stable under concurrency: %v", ids)
		}
	}
}
