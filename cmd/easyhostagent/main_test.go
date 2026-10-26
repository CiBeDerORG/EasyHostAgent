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
	t.Setenv("EASYHOSTAGENT_DATA_DIR", "/tmp/agent-test-state")
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/tmp/agent-test-apps")
	var out, errors bytes.Buffer
	if code := run([]string{"-check-config"}, &out, &errors); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errors.String())
	}
	if !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("unexpected stdout: %q", out.String())
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
