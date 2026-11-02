package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalBundleApprovalFlagsFailClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("EASYHOSTAGENT_DATA_DIR", dir)
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	hash := strings.Repeat("a", 64)
	source := "22222222-2222-4222-8222-222222222222"
	revision := strings.Repeat("b", 40)
	invalid := [][]string{
		{"-approve-bundle", hash},
		{"-approve-bundle", hash, "-source-id", source, "-app-id", "sub2api"},
		{"-approve-bundle", hash, "-source-id", source, "-app-id", "sub2api", "-git-revision", revision, "-pairing-code"},
		{"-source-id", source},
		{"-check-bundle", hash, "-revoke-bundle", hash},
		{"-check-config", "-approve-bundle", hash},
	}
	for _, args := range invalid {
		var stdout, stderr bytes.Buffer
		if got := run(args, &stdout, &stderr); got != 2 {
			t.Fatalf("缺少本地显式条件的命令 %v 不应执行: %d %s", args, got, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "approvals")); !os.IsNotExist(err) {
		t.Fatal("无效命令不应该创建批准目录")
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-approve-bundle", hash, "-source-id", source, "-app-id", "sub2api", "-git-revision", revision}, &stdout, &stderr); got != 1 {
		t.Fatalf("没有有效缓存仍然批准成功: %d %s", got, stdout.String())
	}
	if strings.Contains(stdout.String(), "Local approval saved") {
		t.Fatal("没有缓存被标记为本机批准")
	}
}
