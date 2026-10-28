// Package httpapi exposes the Agent discovery and one-time pairing endpoints.
// No Docker or task execution routes are available in T01-04.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
 "github.com/CiBeDerORG/EasyHostAgent/internal/host"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func NewHandler(state identity.State, platform, version string, stores ...*pairing.Store) http.Handler {
 return NewHandlerWithHost(state, platform, version, nil, stores...)
}

func NewHandlerWithHost(state identity.State, platform, version string, service *host.Service, stores ...*pairing.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		sendJSON(w, map[string]any{"status": "ready", "protocol_version": 1})
	})
	mux.HandleFunc("/v1/identity", func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		host, _ := os.Hostname()
		arch := runtime.GOARCH
		if arch != "amd64" && arch != "arm64" {
			arch = "other"
		}
		sendJSON(w, map[string]any{
			"protocol_version": 1, "agent_id": state.AgentID, "hostname": host,
			"platform": platform, "arch": arch, "version": version,
			"tls_spki_sha256": state.SPKISHA256,
			"observed_at":     time.Now().UTC().Format(time.RFC3339),
		})
	})
	// A nil store is allowed for T01-03 legacy discovery tests only.
	// Production must pass a persistent store.
	if len(stores) != 0 && stores[0] != nil {
		store := stores[0]
  if service != nil { registerHost(mux, state.AgentID, store, service) }
		mux.HandleFunc("/v1/pairings", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
				return
			}
			contentType, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if e != nil || contentType != "application/json" {
				sendError(w, http.StatusBadRequest, "invalid_content_type", "JSON body required")
				return
			}
			var request struct {
				ControllerID string `json:"controller_id"`
				DeviceID     string `json:"device_id"`
				Code         string `json:"pairing_code"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if e := decoder.Decode(&request); e != nil {
				sendError(w, http.StatusBadRequest, "invalid_request", "Invalid pairing request")
				return
			}
			var extra any
			if e := decoder.Decode(&extra); !errors.Is(e, io.EOF) {
				sendError(w, http.StatusBadRequest, "invalid_request", "Expected one JSON object")
				return
			}
			result, e := store.Exchange(request.ControllerID, request.DeviceID, request.Code)
			if e != nil {
				switch {
				case errors.Is(e, pairing.ErrRateLimited):
					sendError(w, http.StatusTooManyRequests, "pairing_attempts_exhausted", "Generate a new code on the host")
				case errors.Is(e, pairing.ErrInvalidCode):
					sendError(w, http.StatusUnauthorized, "invalid_pairing_code", "Pairing code not accepted")
				case errors.Is(e, pairing.ErrInvalidIDs):
					sendError(w, http.StatusUnprocessableEntity, "invalid_identity", "Invalid controller or device identifier")
				case errors.Is(e, pairing.ErrNoCode), errors.Is(e, pairing.ErrExpired):
					sendError(w, http.StatusForbidden, "pairing_window_closed", "Open a new local pairing window")
				default:
					// Do not leak filesystem paths or stored state details.
					sendError(w, http.StatusServiceUnavailable, "pairing_unavailable", "Pairing unavailable")
				}
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(result)
		})
		mux.Handle("/v1/pairings/current", auth.RequireController(state.AgentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				sendJSON(w, map[string]any{
					"agent_id":      state.AgentID,
					"controller_id": r.Header.Get("X-EHA-Controller-ID"),
					"authorized":    true,
				})
				return
			}
			if r.Method != http.MethodDelete {
				w.Header().Set("Allow", "GET, DELETE")
				sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
				return
			}
			token := r.Header.Get("Authorization")
			token = token[len("Bearer "):] // RequireController validates prefix first.
			if e := store.Revoke(r.Header.Get("X-EHA-Controller-ID"), state.AgentID, token); e != nil {
				sendError(w, http.StatusServiceUnavailable, "revoke_failed", "Revocation could not be confirmed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-EHA-Agent-ID", state.AgentID)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS == nil {
			sendError(w, http.StatusBadRequest, "tls_required", "HTTPS is required")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func onlyGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" {
		return true
	}
	w.Header().Set("Allow", "GET")
	sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	return false
}
func sendJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(data)
}
func sendError(w http.ResponseWriter, code int, id, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": id, "message": msg, "retryable": false})
}
