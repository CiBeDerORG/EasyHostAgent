package reverse

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
)

func TestReverseRequiresHTTPSAndStrongToken(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ url, token string }{
		{"http://controller.example.com", strings.Repeat("a", 64)},
		{"https://controller.example.com", "short"},
		{"https://user@example.com", strings.Repeat("a", 64)},
		{"https://controller.example.com/untrusted-path", strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		c, err := New(tc.url, tc.token, dir, "linux", "test", identity.State{}, nil, nil, slog.Default())
		if err == nil || c != nil {
			t.Fatalf("应该拒绝不安全配置 %q: %+v", tc.url, err)
		}
	}
}

func TestReverseTokenFileMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "reverse-bootstrap.token")
	t.Setenv("EASYHOSTAGENT_REVERSE_BOOTSTRAP_TOKEN_FILE", file)
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 64)), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := New("https://controller.example.com", "", dir, "linux", "test", identity.State{}, nil, nil, slog.Default())
	if err == nil || c != nil {
		t.Fatal("拒绝世界可读的接入密钥文件")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	// 权限正确后会继续验证身份私钥，不能把空 identity 静默当成合法 Agent。
	c, err = New("https://controller.example.com", "", dir, "linux", "test", identity.State{}, nil, nil, slog.Default())
	if err == nil || c != nil || !strings.Contains(err.Error(), "身份私钥") {
		t.Fatalf("密钥文件读取后应进入身份检查: %v", err)
	}
}
