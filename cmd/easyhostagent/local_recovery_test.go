package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStaleTaskRecoveryRequiresFullExplicitConfirmation(t *testing.T) {
	t.Setenv("EASYHOSTAGENT_DATA_DIR", filepath.Join(t.TempDir(), "private"))
	t.Setenv("EASYHOSTAGENT_SOFTWARE_ROOT", "/opt/software")
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	controller := "22222222-2222-4222-8222-222222222222"
	for _, args := range [][]string{
		{"-recover-task", id},
		{"-recover-task", id, "-expected-controller", controller},
		{"-expected-controller", controller},
		{"-recover-task", id, "-expected-controller", controller, "-expected-app", "sub2api", "-pairing-code"},
		{"-recover-task", id, "-expected-controller", controller, "-expected-app", "sub2api", "-approve-bundle", strings.Repeat("a", 64)},
	} {
		var stdout, stderr bytes.Buffer
		if result := run(args, &stdout, &stderr); result != 2 {
			t.Fatalf("不完整或冲突的本机恢复参数被执行: %v return=%d stderr=%s", args, result, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	result := run([]string{"-recover-task", id, "-expected-controller", controller,
		"-expected-app", "sub2api"}, &stdout, &stderr)
	if result != 1 || strings.Contains(stdout.String(), "Stale task recorded") {
		t.Fatalf("不存在的任务被当成已经恢复: result=%d output=%q error=%q",
			result, stdout.String(), stderr.String())
	}
}
