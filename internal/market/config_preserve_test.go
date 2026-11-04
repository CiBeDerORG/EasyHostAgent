package market

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPortainerFields() []field {
	minLength := 12
	return []field{
		{Key: "PORTAINER_HTTP_PORT", Type: "port", Target: "env", Required: true},
		{Key: "PORTAINER_ADMIN_PASSWORD", Type: "secret", Target: "secrets", Required: true, MinLength: &minLength},
	}
}

func testPortainerConfigRoot(t *testing.T, existingSecret string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deploy")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PORTAINER_HTTP_PORT=\"7004\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if existingSecret != "" {
		if err := os.WriteFile(filepath.Join(dir, ".secrets"), []byte(existingSecret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPrepareConfigKeepsExistingCredentialDespiteSubmittedPassword(t *testing.T) {
	const original = "PORTAINER_ADMIN_PASSWORD=\"existing-password-123\"\n"
	root := testPortainerConfigRoot(t, original)
	secrets := filepath.Join(root, "deploy", ".secrets")
	for _, candidate := range []string{"a-different-password-456", "", "existing-password-123"} {
		values, err := prepareConfig(root, "linux", testPortainerFields(), map[string]string{
			"PORTAINER_HTTP_PORT":     "7005",
			"PORTAINER_ADMIN_PASSWORD": candidate,
		}, true)
		if err != nil {
			t.Fatalf("reinstall with submitted secret must succeed: %v", err)
		}
		if values["PORTAINER_ADMIN_PASSWORD"] != "existing-password-123" || values["PORTAINER_HTTP_PORT"] != "7005" {
			t.Fatalf("reinstall changed credentials or ignored ordinary config: %#v", values)
		}
		raw, err := os.ReadFile(secrets)
		if err != nil || string(raw) != original {
			t.Fatalf("reinstall changed original credential file: %v", err)
		}
	}
	withoutSecret, err := prepareConfig(root, "linux", testPortainerFields(), map[string]string{
		"PORTAINER_HTTP_PORT": "7006",
	}, true)
	if err != nil || withoutSecret["PORTAINER_ADMIN_PASSWORD"] != "existing-password-123" {
		t.Fatalf("reinstall without submitting secret must reuse existing value: %#v, %v", withoutSecret, err)
	}
}

func TestPrepareConfigFirstInstallRequiresSecret(t *testing.T) {
	root := testPortainerConfigRoot(t, "")
	_, err := prepareConfig(root, "linux", testPortainerFields(), map[string]string{
		"PORTAINER_HTTP_PORT": "7004",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "PORTAINER_ADMIN_PASSWORD") {
		t.Fatalf("new install with no password must fail validation: %v", err)
	}
	values, err := prepareConfig(root, "linux", testPortainerFields(), map[string]string{
		"PORTAINER_HTTP_PORT":     "7004",
		"PORTAINER_ADMIN_PASSWORD": "new-valid-password-123",
	}, true)
	if err != nil || values["PORTAINER_ADMIN_PASSWORD"] != "new-valid-password-123" {
		t.Fatalf("first install did not accept valid secret: %#v, %v", values, err)
	}
}

func TestPrepareConfigRejectsInvalidExistingCredential(t *testing.T) {
	root := testPortainerConfigRoot(t, "PORTAINER_ADMIN_PASSWORD=\"short\"\n")
	_, err := prepareConfig(root, "linux", testPortainerFields(), map[string]string{
		"PORTAINER_HTTP_PORT":     "7004",
		"PORTAINER_ADMIN_PASSWORD": "another-long-password",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "PORTAINER_ADMIN_PASSWORD") {
		t.Fatalf("invalid existing secret must not be silently replaced: %v", err)
	}
}
