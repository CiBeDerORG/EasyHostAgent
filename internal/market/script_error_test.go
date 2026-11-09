package market

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestScriptFailureMessageUsesActualStderr(t *testing.T) {
	message := scriptFailureMessage(1,
		"EasyTier 需要 /dev/net/tun，当前宿主机没有可用 TUN 设备。\n",
		"[1/5] 校验 Compose 配置...\n", nil)
	if !strings.Contains(message, "脚本退出码 1") || !strings.Contains(message, "/dev/net/tun") {
		t.Fatalf("未展示脚本原始失败原因: %q", message)
	}
}

func TestScriptFailureMessageFallsBackToStdout(t *testing.T) {
	message := scriptFailureMessage(17, "",
		"[1/5] 校验中\nError response from daemon: connection refused\n", nil)
	if !strings.Contains(message, "脚本退出码 17") || !strings.Contains(message, "connection refused") {
		t.Fatalf("未提取 stdout 中的错误原因: %q", message)
	}
}

func TestScriptFailureMessageFiltersSecrets(t *testing.T) {
	const secret = "localVerySecretValue"
	message := scriptFailureMessage(1,
		"failed: password="+secret+"\n"+
			"Error: authentication token=anotherSecret\n"+
			"Error: Get https://host.invalid/?auth=private-value\n"+
			"Error: failed to connect to Docker daemon\n",
		"failed: APP_PASSWORD="+secret+"\n",
		map[string]string{"APP_PASSWORD": secret})
	if strings.Contains(message, secret) || strings.Contains(message, "anotherSecret") ||
		strings.Contains(message, "private-value") {
		t.Fatalf("对控制端泄露了脚本中的凭据: %q", message)
	}
	if !strings.Contains(message, "Docker daemon") {
		t.Fatalf("敏感信息过滤误删无凭据的错误行: %q", message)
	}
}

func TestScriptFailureMessageDoesNotRevealOrdinaryOutput(t *testing.T) {
	message := scriptFailureMessage(1, "", "initial password: a-secret-value\n操作已启动\n", nil)
	if strings.Contains(message, "a-secret-value") ||
		!strings.Contains(message, "未捕获到可安全展示的错误信息") {
		t.Fatalf("普通输出不应作为错误信息曝光: %q", message)
	}
}

func TestScriptErrorTailBoundsAndLimitsDisplay(t *testing.T) {
	var output scriptErrorTail
	longLine := "error: " + strings.Repeat("x", maxScriptOutputBytes+100)
	if n, err := output.Write([]byte(longLine)); err != nil || n != len(longLine) {
		t.Fatalf("写入脚本输出失败: %d %v", n, err)
	}
	if len(output.data) != maxScriptOutputBytes {
		t.Fatalf("输出未按上限裁剪: %d", len(output.data))
	}
	_, _ = output.Write([]byte("\nfailed to start: " + strings.Repeat("中", 500)))
	if len(output.data) > maxScriptOutputBytes {
		t.Fatal("连续写入超出内存限制")
	}
	message := scriptFailureMessage(1, output.String(), "", nil)
	if !strings.Contains(message, "failed to start") || len([]rune(message)) > 470 {
		t.Fatalf("错误详情未限制展示长度: %q", message)
	}
}

func TestScriptFailureMessageCapturesRealShellError(t *testing.T) {
	var stdout, stderr scriptErrorTail
	cmd := exec.Command("/bin/sh", "-c", "printf '配置错误：缺少 TUN 设备\\n' >&2; exit 9")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("预期脚本退出错误，实际: %v", err)
	}
	message := scriptFailureMessage(exit.ExitCode(), stderr.String(), stdout.String(), nil)
	if !strings.Contains(message, "缺少 TUN 设备") || !strings.Contains(message, "退出码 9") {
		t.Fatalf("未提取实际脚本错误输出: %q", message)
	}
}
