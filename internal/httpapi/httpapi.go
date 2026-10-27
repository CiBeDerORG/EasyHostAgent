// Package httpapi exposes only the two unauthenticated discovery routes in
// T01-03. Pairing and all privileged endpoints are added in later tasks.
package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
)

func NewHandler(state identity.State, platform, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health",func(w http.ResponseWriter,r *http.Request){
		if !onlyGET(w,r) {return}
		sendJSON(w,map[string]any{"status":"ready","protocol_version":1})
	})
	mux.HandleFunc("/v1/identity",func(w http.ResponseWriter,r *http.Request){
		if !onlyGET(w,r) {return}
		host, _ := os.Hostname()
		arch:=runtime.GOARCH
		if arch!="amd64"&&arch!="arm64" {arch="other"}
		sendJSON(w,map[string]any{
			"protocol_version":1, "agent_id":state.AgentID, "hostname":host,
			"platform":platform,"arch":arch,"version":version,
			"tls_spki_sha256":state.SPKISHA256,
			"observed_at":time.Now().UTC().Format(time.RFC3339),
		})
	})
	return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		w.Header().Set("X-EHA-Agent-ID",state.AgentID)
		w.Header().Set("Cache-Control","no-store")
		w.Header().Set("X-Content-Type-Options","nosniff")
		if r.TLS==nil {
			sendError(w,http.StatusBadRequest,"tls_required","HTTPS is required")
			return
		}
		mux.ServeHTTP(w,r)
	})
}
func onlyGET(w http.ResponseWriter,r *http.Request)bool{
	if r.Method=="GET"{return true}
	w.Header().Set("Allow","GET")
	sendError(w,http.StatusMethodNotAllowed,"method_not_allowed","Method not allowed")
	return false
}
func sendJSON(w http.ResponseWriter,data any){
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(data)
}
func sendError(w http.ResponseWriter,code int,id,msg string){
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code":id,"message":msg,"retryable":false})
}
