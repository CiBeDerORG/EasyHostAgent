package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/host"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func registerHost(mux *http.ServeMux, agentID string, store *pairing.Store, service *host.Service) {
	mux.Handle("/v1/host", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		snap, err := service.Snapshot(r.Context())
		if err != nil {
			sendError(w, http.StatusServiceUnavailable, "host_unavailable", "Host snapshot unavailable")
			return
		}
		sendJSON(w, snap)
	})))
	mux.Handle("/v1/host/settings", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.Header().Set("Allow", "PATCH")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || typ != "application/json" {
			sendError(w, 400, "invalid_content_type", "JSON body required")
			return
		}
		var input struct {
			SoftwareRoot string `json:"software_root"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			sendError(w, 400, "invalid_request", "Invalid settings request")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			sendError(w, 400, "invalid_request", "Expected one JSON object")
			return
		}
		_, err = service.SetRoot(input.SoftwareRoot)
		if errors.Is(err, host.ErrInvalidRoot) {
			sendError(w, 422, "invalid_software_root", "Use an absolute real directory path without symlinks")
			return
		}
		if errors.Is(err, host.ErrManagedApps) {
			sendError(w, 409, "managed_applications_exist", "Migrate and verify managed applications before changing the root")
			return
		}
		if err != nil {
			sendError(w, 503, "settings_unavailable", "Software root settings unavailable")
			return
		}
		snap, err := service.Snapshot(r.Context())
		if err != nil {
			sendError(w, 503, "host_unavailable", "Host snapshot unavailable")
			return
		}
		sendJSON(w, snap)
	})))
}
