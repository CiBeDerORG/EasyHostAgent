package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func TestBundleHTTPSAuthorizationAndStatus(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "agent")
	state, err := identity.LoadOrCreate(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := pairing.New(dataDir, state.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	controller := "11111111-1111-4111-8111-111111111111"
	host := "22222222-2222-4222-8222-222222222222"
	code, _, err := grant.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := grant.Exchange(controller, host, code)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandlerWithBundles(state, "linux", "dev", nil, nil, cache, grant))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{state.Certificate}}
	server.StartTLS()
	defer server.Close()
	// Use a real self-signed Agent HTTPS server; httptest trusts its own cert.
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	f, err := writer.Create("sub2api/deploy/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("#!/bin/sh\necho hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(body.Bytes())
	digest := hex.EncodeToString(checksum[:])
	path := "/v1/bundles/" + digest
	call := func(method, url string, requestBody []byte, authorized bool) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+url, bytes.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodPut {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		if authorized {
			req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			req.Header.Set("X-EHA-Controller-ID", controller)
			req.Header.Set("X-EHA-Agent-ID", state.AgentID)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	assert := func(method, url string, content []byte, auth bool, status int) {
		t.Helper()
		res := call(method, url, content, auth)
		defer res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("%s %s got %d want %d", method, url, res.StatusCode, status)
		}
		if res.Header.Get("X-EHA-Agent-ID") != state.AgentID {
			t.Fatal("Agent response identity absent")
		}
	}
	assert(http.MethodHead, path, nil, false, http.StatusUnauthorized)
	assert(http.MethodHead, path, nil, true, http.StatusNotFound)
	assert(http.MethodPut, path, body.Bytes(), false, http.StatusUnauthorized)
	res := call(http.MethodPut, path, body.Bytes(), true)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first bundle upload failed: %d", res.StatusCode)
	}
	var receipt struct {
		AgentID       string `json:"agent_id"`
		SHA256        string `json:"sha256"`
		SizeBytes     int64  `json:"size_bytes"`
		AlreadyExists bool   `json:"already_exists"`
	}
	if err := json.NewDecoder(res.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if receipt.AgentID != state.AgentID || receipt.SHA256 != digest || receipt.SizeBytes != int64(body.Len()) || receipt.AlreadyExists {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	assert(http.MethodHead, path, nil, true, http.StatusNoContent)
	assert(http.MethodPut, path, body.Bytes(), true, http.StatusOK)
	assert(http.MethodPut, "/v1/bundles/"+strings.Repeat("0", 64), body.Bytes(), true, http.StatusUnprocessableEntity)
	assert(http.MethodPut, "/v1/bundles/"+digest, []byte("invalid"), false, http.StatusUnauthorized)
	assert(http.MethodHead, "/v1/bundles/not-a-sha", nil, true, http.StatusBadRequest)
	assert(http.MethodGet, path, nil, true, http.StatusMethodNotAllowed)
}
