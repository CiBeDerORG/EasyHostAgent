package httpapi

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func registerBundles(mux *http.ServeMux,agentID string,pairingStore *pairing.Store,cache *bundles.Store) {
	mux.Handle("/v1/bundles/{sha256}",auth.RequireController(agentID,pairingStore,http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Method!=http.MethodHead && r.Method!=http.MethodPut {
			w.Header().Set("Allow","HEAD, PUT")
			sendError(w,http.StatusMethodNotAllowed,"method_not_allowed","Method not allowed")
			return
		}
		digest:=r.PathValue("sha256")
		if !bundles.ValidDigest(digest){
			sendError(w,http.StatusBadRequest,"invalid_digest","Expected lowercase SHA-256")
			return
		}
		if r.Method==http.MethodHead {
			exists,_,err:=cache.Has(digest)
			if err!=nil {
				sendError(w,http.StatusConflict,"bundle_cache_unavailable","Bundle cache requires local repair")
				return
			}
			if !exists {
				sendError(w,http.StatusNotFound,"bundle_missing","Bundle not present")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		typ,_,err:=mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err!=nil||typ!="application/octet-stream"{
			sendError(w,http.StatusBadRequest,"invalid_content_type","Use application/octet-stream")
			return
		}
		if r.ContentLength>bundles.MaxBundleSize {
			sendError(w,http.StatusRequestEntityTooLarge,"bundle_too_large","Bundle limit exceeded")
			return
		}
		stream:=http.MaxBytesReader(w,r.Body,bundles.MaxBundleSize)
		defer stream.Close()
		size,exists,err:=cache.Put(r.Context(),digest,stream)
		if err!=nil {
			switch {
			case errors.Is(err,bundles.ErrTooLarge):
				sendError(w,http.StatusRequestEntityTooLarge,"bundle_too_large","Bundle limit exceeded")
			case errors.Is(err,bundles.ErrInvalidArchive),errors.Is(err,bundles.ErrDigestMismatch):
				sendError(w,http.StatusUnprocessableEntity,"invalid_bundle","Bundle failed verification")
			case errors.Is(err,bundles.ErrCorruptStored):
				sendError(w,http.StatusConflict,"bundle_cache_corrupt","Manual cache repair required")
			default:
				sendError(w,http.StatusServiceUnavailable,"bundle_unavailable","Bundle could not be saved")
			}
			return
		}
		status:=http.StatusCreated
		if exists{status=http.StatusOK}
		w.Header().Set("Content-Type","application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_id":agentID,"sha256":digest,"size_bytes":size,"already_exists":exists,
		})
	})))
}
