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
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/tmp/agent-test-apps")
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
    if err := os.MkdirAll(dataDir, 0700); err != nil { t.Fatal(err) }
    t.Setenv("EASYHOSTAGENT_DATA_DIR", dataDir)
    t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
    if err := os.WriteFile(filepath.Join(dataDir, "host-settings.json"), []byte(`{"software_root":"/tmp/persisted-apps"}`), 0600); err != nil { t.Fatal(err) }
    var out, errors bytes.Buffer
    if code := run([]string{"-check-config"}, &out, &errors); code != 0 ||
        !strings.Contains(out.String(), "software_root=/tmp/persisted-apps") {
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
