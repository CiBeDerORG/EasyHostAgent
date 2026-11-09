package market

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxScriptOutputBytes     = 12 << 10
	maxScriptDiagnosticRunes = 200
)

// scriptErrorTail 只在内存中保留脚本输出末尾，避免安装日志无限增长。
type scriptErrorTail struct {
	data []byte
}

func (b *scriptErrorTail) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxScriptOutputBytes {
		b.data = append(b.data[:0], p[n-maxScriptOutputBytes:]...)
		return n, nil
	}
	if overflow := len(b.data) + n - maxScriptOutputBytes; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *scriptErrorTail) String() string {
	return string(b.data)
}

var (
	scriptDiagnosticPattern = regexp.MustCompile("(?i)(error|failed|failure|fatal|denied|invalid|missing|not found|no such|cannot|unable|refused|timed out|timeout|unavailable|forbidden|unknown|unexpected|错误|失败|缺少|无法|不存在|没有|拒绝|无效|超时|必须|未设置|不支持|异常|不可用)")
	scriptSensitivePattern  = regexp.MustCompile("(?i)(password|passwd|secret|token|private[_-]?key|api[_-]?key|authorization|bearer|credential|pairing[_-]?code)")
	scriptANSIPattern       = regexp.MustCompile("\x1b\\[[0-9;]*[[:alpha:]]")
)

// 脚本可能输出初始口令或访问令牌；只提取含错误特征的行，敏感行整行丢弃。
// 优先 stderr，必要时从 stdout 提取诊断信息，不向控制端转发完整日志。
func scriptFailureMessage(code int, stderr, stdout string, values map[string]string) string {
	for _, output := range []string{stderr, stdout} {
		output = scriptANSIPattern.ReplaceAllString(output, "")
		output = strings.ReplaceAll(output, "\r", "\n")
		var diagnostics []string
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, line))
			if line == "" || !scriptDiagnosticPattern.MatchString(line) ||
				scriptSensitivePattern.MatchString(line) ||
				strings.Contains(line, "-----BEGIN") ||
				(strings.Contains(line, "://") && (strings.Contains(line, "?") || strings.Contains(line, "@"))) {
				continue
			}
			sensitive := false
			for key, value := range values {
				if value != "" && scriptSensitivePattern.MatchString(key) && strings.Contains(line, value) {
					sensitive = true
					break
				}
			}
			if sensitive {
				continue
			}
			runes := []rune(line)
			if len(runes) > maxScriptDiagnosticRunes {
				line = string(runes[:maxScriptDiagnosticRunes]) + "…"
			}
			if len(diagnostics) == 0 || diagnostics[len(diagnostics)-1] != line {
				diagnostics = append(diagnostics, line)
			}
		}
		if len(diagnostics) != 0 {
			if len(diagnostics) > 2 {
				diagnostics = diagnostics[len(diagnostics)-2:]
			}
			return fmt.Sprintf("脚本退出码 %d：%s", code, strings.Join(diagnostics, "；"))
		}
	}
	return fmt.Sprintf("脚本退出码 %d（未捕获到可安全展示的错误信息）", code)
}
