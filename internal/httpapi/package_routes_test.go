package httpapi

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/deploypackage"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func makeRouteTestPackage(t *testing.T) ([]byte, string) {
	t.Helper()
	type item struct {
		name string
		data string
		mode int64
	}
	assets := []item{
		{"portainer/deploy/docker-compose.yaml", "name: portainer\nservices: {}\n", 0644},
		{"portainer/deploy/install.sh", "#!/bin/sh\nexit 0\n", 0755},
	}
	manifest := []byte("schema_version: 1\nid: portainer\n")
	m := sha256.Sum256(manifest)
	env := deploypackage.Envelope{
		FormatVersion: 1, AppID: "portainer",
		GitRevision:    "0123456789abcdef0123456789abcdef01234567",
		ManifestPath:   "manifest/app.yaml",
		ManifestSHA256: hex.EncodeToString(m[:]),
		AllowedActions: map[string]string{"install": "script"},
	}
	for _, asset := range assets {
		hash := sha256.Sum256([]byte(asset.data))
		mode := "0644"
		if asset.mode == 0755 {
			mode = "0755"
		}
		env.Files = append(env.Files, deploypackage.File{
			Source: asset.name, Destination: asset.name, SHA256: hex.EncodeToString(hash[:]),
			Size: int64(len(asset.data)), Mode: mode,
		})
	}
	metadata, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	inputs := []item{{"bundle.json", string(metadata), 0644}, {"manifest/app.yaml", string(manifest), 0644}}
	inputs = append(inputs, assets...)
	for _, asset := range inputs {
		h := &tar.Header{Name: asset.name, Mode: asset.mode, Size: int64(len(asset.data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := writer.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(asset.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}
func TestDeployPackageTransferRequiresPairedControllerAndNeverInstalls(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	state, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	authStore, err := pairing.New(dir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	packageStore, err := deploypackage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(NewHandlerWithPackages(state, "linux", "test", nil, nil, packageStore, authStore))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	srv.StartTLS()
	defer srv.Close()
	bundle, digest := makeRouteTestPackage(t)
	request := func(method, url, controller, bearer, hash string, payload []byte) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+url, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		req.Header.Set("X-EHA-Controller-ID", controller)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/x-tar")
		req.Header.Set("X-EHA-Package-SHA256", hash)
		response, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	if code, _ := request("POST", "/v1/deploy/packages", controllerA, "invalid", digest, bundle); code != 401 {
		t.Fatalf("anonymous package accepted: %d", code)
	}
	code, _, err := authStore.Generate()
	if err != nil {
		t.Fatal(err)
	}
	paired, result := postPair(t, srv,
		`{"controller_id":"`+controllerA+`","device_id":"`+deviceA+`","pairing_code":"`+code+`"}`, "application/json")
	if paired.StatusCode != 201 {
		t.Fatalf("pairing: %+v", result)
	}
	token := result["access_token"].(string)
	if code, _ := request("POST", "/v1/deploy/packages", controllerB, token, digest, bundle); code != 401 {
		t.Fatalf("other controller accepted: %d", code)
	}
	badDigest := "0" + digest[1:]
	if badDigest == digest {
		badDigest = "1" + digest[1:]
	}
	if code, _ := request("POST", "/v1/deploy/packages", controllerA, token, badDigest, bundle); code != 422 {
		t.Fatalf("incorrect digest accepted: %d", code)
	}
	accepted, body := request("POST", "/v1/deploy/packages", controllerA, token, digest, bundle)
	if accepted != 201 || body["install_enabled"] != false || body["ready_for_agent"] != false {
		t.Fatalf("package acceptance must be storage-only: %d %+v", accepted, body)
	}
	retry, body := request("POST", "/v1/deploy/packages", controllerA, token, digest, bundle)
	if retry != 200 || body["reused"] != true {
		t.Fatalf("idempotent retry failed: %d %+v", retry, body)
	}
	route := "/v1/deploy/packages/portainer/0123456789abcdef0123456789abcdef01234567"
	if status, body := request("GET", route, controllerA, token, digest, nil); status != 200 || body["package_sha256"] != digest {
		t.Fatalf("stored receipt missing: %d %+v", status, body)
	}
	if status, _ := request("POST", route, controllerA, token, digest, nil); status != 405 {
		t.Fatalf("receipt route should be read-only: %d", status)
	}
}
