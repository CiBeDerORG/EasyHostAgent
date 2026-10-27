// Package auth provides a fail-closed bearer authentication boundary for
// future privileged Agent API handlers. Pairing/token provisioning is T01-04.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Verifier is implemented by a persistent paired-controller credential store
// in T01-04; it must ensure the token belongs to the given controller and agent.
type Verifier interface {
	Authorized(controllerID, agentID, token string) bool
}
type VerifyFunc func(controllerID, agentID, token string) bool

func (fn VerifyFunc) Authorized(c, a, token string) bool { return fn(c, a, token) }

// EqualToken avoids timing leaks even when token lengths differ.
func EqualToken(actual, expected string) bool {
	a := sha256.Sum256([]byte(actual))
	b := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// RequireController is opt-in for each privileged endpoint. When there is no
// verifier (until T01-04), no request can get privileged access.
func RequireController(agentID string, verifier Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil {
			writeDenied(w, http.StatusBadRequest, "tls_required")
			return
		}
		ctrl := r.Header.Get("X-EHA-Controller-ID")
		claimed := r.Header.Get("X-EHA-Agent-ID")
		if ctrl == "" || claimed != agentID || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeDenied(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(token) < 32 || strings.TrimSpace(token) != token || verifier == nil || !verifier.Authorized(ctrl, agentID, token) {
			writeDenied(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func writeDenied(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": code, "message": "Request not authorized", "retryable": false,
	})
}
