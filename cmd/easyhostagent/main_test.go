package main

import (
	"bytes"
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
	t.Setenv("EASYHOSTAGENT_DATA_DIR", t.TempDir())
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/tmp/agent-test-apps")
	var out, errors bytes.Buffer
	if code := run([]string{"-check-config"}, &out, &errors); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errors.String())
	}
	if !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("stdout=%q", out.String())
	}
}
func TestLocalIdentityCommandIsStable(t *testing.T) {
	t.Setenv("EASYHOSTAGENT_DATA_DIR", t.TempDir())
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
