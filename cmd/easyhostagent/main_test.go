package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo"
)

func TestVersionExitsWithoutLaunchingService(t *testing.T) {
	var out, errors bytes.Buffer
	code := run([]string{"-version"}, &out, &errors)
	if code != 0 || !strings.Contains(out.String(), buildinfo.Version) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errors.String())
	}
}
func TestCheckConfig(t *testing.T) {
	t.Setenv("EASYHOSTAGENT_DATA_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", filepath.Join(t.TempDir(), "agent-test-apps"))
	var out, errors bytes.Buffer
	if code := run([]string{"-check-config"}, &out, &errors); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errors.String())
	}
	if !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("stdout=%q", out.String())
	}
}
func TestCheckConfigShowsSavedSoftwareRoot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EASYHOSTAGENT_DATA_DIR", dataDir)
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	savedRoot := filepath.Join(t.TempDir(), "persisted-apps")
	if err := os.WriteFile(filepath.Join(dataDir, "host-settings.json"), []byte(`{"software_root":"`+savedRoot+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errors bytes.Buffer
	if code := run([]string{"-check-config"}, &out, &errors); code != 0 ||
		!strings.Contains(out.String(), "software_root="+savedRoot) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), errors.String())
	}
}
func TestLocalIdentityCommandIsStable(t *testing.T) {
	t.Setenv("EASYHOSTAGENT_DATA_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	var first, second, errors bytes.Buffer
	if code := run([]string{"-identity"}, &first, &errors); code != 0 {
		t.Fatalf("first code=%d: %s", code, errors.String())
	}
	if code := run([]string{"-identity"}, &second, &errors); code != 0 {
		t.Fatalf("second code=%d: %s", code, errors.String())
	}
	if first.String() != second.String() || !strings.Contains(first.String(), "TLS SPKI SHA-256:") {
		t.Fatalf("unstable local identity: %q vs %q", first.String(), second.String())
	}
}
func TestInteractiveTokenPairing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-state")
	t.Setenv("EASYHOSTAGENT_DATA_DIR", dir)
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	t.Setenv("EASYHOSTAGENT_CONTROLLER_URL", "https://controller.example.com")
	var out, errors bytes.Buffer
	if code := run([]string{"-pair-status"}, &out, &errors); code != 0 || !strings.Contains(out.String(), "paired=false") {
		t.Fatalf("unexpected initial status: code=%d out=%q err=%q", code, out.String(), errors.String())
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = old
		_ = reader.Close()
	})
	token := strings.Repeat("ab", 32)
	if _, err := writer.WriteString(token + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	out.Reset()
	errors.Reset()
	if code := run([]string{"-pair"}, &out, &errors); code != 0 {
		t.Fatalf("pair failed: %d %s", code, errors.String())
	}
	if strings.Contains(out.String(), token) || strings.Contains(errors.String(), token) {
		t.Fatal("one-time token leaked into CLI output")
	}
	info, err := os.Stat(filepath.Join(dir, "reverse-bootstrap.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private token file: %v %v", info, err)
	}
	out.Reset()
	errors.Reset()
	if code := run([]string{"-pair-status"}, &out, &errors); code != 0 || !strings.Contains(out.String(), "paired=true") {
		t.Fatalf("token was not retained: code=%d out=%q err=%q", code, out.String(), errors.String())
	}
}

func TestBadFlagsAndConfig(t *testing.T) {
	var out, errors bytes.Buffer
	if code := run([]string{"--unknown"}, &out, &errors); code != 2 {
		t.Fatalf("expected flag error, got %d", code)
	}
	out.Reset()
	errors.Reset()
	t.Setenv("EASYHOSTAGENT_DATA_DIR", "relative")
	if code := run(nil, &out, &errors); code != 1 {
		t.Fatalf("expected config error, got %d", code)
	}
}

func TestGenerateLocalPairingCodeDoesNotPrintBearer(t *testing.T) {
	t.Setenv("EASYHOSTAGENT_DATA_DIR", filepath.Join(t.TempDir(), "private"))
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-pairing-code"}, &stdout, &stderr); code != 0 {
		t.Fatalf("failed to generate pairing code: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "TLS SPKI SHA-256:") ||
		!strings.Contains(stdout.String(), "One-time pairing code:") ||
		!strings.Contains(stdout.String(), "Expires (UTC):") {
		t.Fatalf("missing pairing information: %s", stdout.String())
	}
	if strings.Contains(strings.ToLower(stdout.String()), "bearer") {
		t.Fatal("unexpected bearer in local code output")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-pairing-code", "-identity"}, &stdout, &stderr); code != 2 {
		t.Fatalf("conflicting options must be rejected: %d", code)
	}
}
