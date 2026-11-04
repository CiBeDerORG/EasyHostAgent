package market

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFnOSAdministratorOwnedPathScope(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("仅 root Agent 可以信任 fnOS 管理员目录")
	}
	root := "/vol3/1000/software"
	cases := []struct {
		path, softwareRoot, platform string
		uid                          uint32
		allow                        bool
	}{
		{"/vol3/1000", root, "fnos", 1000, true},
		{"/vol3/1000/software", root, "fnos", 1000, true},
		{"/vol3/1000/software/deploy/lib", root, "fnos", 1000, true},
		{"/vol3/1000/software/portainer/deploy/install.sh", root, "fnos", 1000, true},
		{"/vol3", root, "fnos", 1000, false},
		{"/vol3/1000/software-other", root, "fnos", 1000, false},
		{"/vol3/1000/software/portainer", root, "linux", 1000, false},
		{"/vol3/1000/software/portainer", root, "fnos", 1001, false},
		{"/vol3/1001/software/portainer", root, "fnos", 1000, false},
		{"/tmp/1000/software", "/tmp/1000/software", "fnos", 1000, false},
		{"/vol3/1000/software", "/vol3/1000/software/extra", "fnos", 1000, false},
	}
	for _, tc := range cases {
		if got := fnosTrustedOwner(tc.path, tc.softwareRoot, tc.platform, tc.uid); got != tc.allow {
			t.Errorf("fnosTrustedOwner(%q, %q, %q, %d) = %v, want %v",
				tc.path, tc.softwareRoot, tc.platform, tc.uid, got, tc.allow)
		}
	}
}

func TestSoftwareRootRejectsUntrustedParentOnLinux(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root 模拟其他 UID 的父目录")
	}
	parent := filepath.Join(t.TempDir(), "other-user")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(parent, 12345, 12345); err != nil {
		t.Skipf("无法模拟其他 UID: %v", err)
	}
	err := ensureSoftwareRoot(filepath.Join(parent, "software"), "linux")
	if err == nil || !strings.Contains(err.Error(), parent) {
		t.Fatalf("普通 Linux 不得信任其他用户的目录: %v", err)
	}
}

func TestSoftwareRootRejectsSymlinkAndWritableParent(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureSoftwareRoot(filepath.Join(link, "software"), "linux"); err == nil {
		t.Fatal("软件目录不得通过符号链接")
	}
	if err := os.Chmod(real, 0777); err != nil {
		t.Fatal(err)
	}
	if err := ensureSoftwareRoot(filepath.Join(real, "software"), "linux"); err == nil {
		t.Fatal("不得接受组或其他用户可写的目录")
	}
}
