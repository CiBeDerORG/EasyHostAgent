package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func registerMarket(mux *http.ServeMux, agentID string, store *pairing.Store, apps *market.Service) {
	mux.Handle("POST /v1/apps/{action}", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			sendError(w, 415, "content_type", "需要 JSON")
			return
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
		d.DisallowUnknownFields()
		decode := func(v any) bool { return d.Decode(v) == nil && d.Decode(&struct{}{}) == io.EOF }
		controller := r.Header.Get("X-EHA-Controller-ID")
		name := r.PathValue("action")
		if name == "status" {
			var req struct {
				Apps           []market.Ref `json:"apps"`
				IncludeManaged bool         `json:"include_managed"`
			}
			if !decode(&req) {
				sendError(w, 400, "invalid_request", "应用列表无效")
				return
			}
			rows, err := apps.Status(r.Context(), controller, req.Apps, req.IncludeManaged)
			if err != nil {
				sendError(w, 400, "app_status", err.Error())
				return
			}
			sendJSON(w, map[string]any{"apps": rows})
			return
		}
		if name != "install" && name != "update" && name != "uninstall" && name != "adopt" {
			http.NotFound(w, r)
			return
		}
		var req market.Request
		if !decode(&req) {
			sendError(w, 400, "invalid_request", "应用操作参数无效")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := func() bool { return store.Authorized(controller, agentID, token) }
		var rec market.Record
		var err error
		if name == "adopt" {
			rec, err = apps.Adopt(r.Context(), controller, req, authorized)
		} else {
			rec, err = apps.Start(r.Context(), controller, name, req, authorized)
		}
		if err != nil {
			status := 422
			switch {
			case errors.Is(err, market.ErrDenied):
				status = 403
			case errors.Is(err, market.ErrBusy), errors.Is(err, market.ErrConflict):
				status = 409
			}
			sendError(w, status, "app_action", err.Error())
			return
		}
		sendJSON(w, rec)
	})))
}
