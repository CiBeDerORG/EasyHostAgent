package reverse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionStatePersistsSecurely(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	endpoint := "https://10.126.0.8:7018"
	if err := SavePromotion(dir, endpoint); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadPromotion(dir)
	if err != nil || !ok || got != endpoint {
		t.Fatalf("promotion state endpoint=%q ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(filepath.Join(dir, promotionFileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("promotion state permissions: info=%v err=%v", info, err)
	}
	if err := ClearPromotion(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadPromotion(dir); err != nil || ok {
		t.Fatalf("promotion state should be cleared: ok=%v err=%v", ok, err)
	}
}

func TestPromotionEndpointRejectsNonOverlayAddresses(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:7018",
		"https://0.0.0.0:7018",
		"https://10.126.0.8",
		"https://example.com:7018",
		"https://10.126.0.8:7018/path",
		"ftp://10.126.0.8:7018",
	} {
		if err := validatePromotionEndpoint(endpoint); err == nil {
			t.Errorf("invalid promotion endpoint accepted: %s", endpoint)
		}
	}
}
