package httpapi

import (
    "errors"
    "mime"
    "net/http"
    "strings"

    "github.com/CiBeDerORG/EasyHostAgent/internal/auth"
    "github.com/CiBeDerORG/EasyHostAgent/internal/deploypackage"
    "github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func registerPackages(mux *http.ServeMux, agentID string, pairingStore *pairing.Store, packages *deploypackage.Store) {
    mux.Handle("/v1/deploy/packages",auth.RequireController(agentID,pairingStore,http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
        if r.Method!=http.MethodPost {
            w.Header().Set("Allow","POST")
            sendError(w,http.StatusMethodNotAllowed,"method_not_allowed","Only POST is supported")
            return
        }
        media,params,err:=mime.ParseMediaType(r.Header.Get("Content-Type"))
        if err!=nil || (media!="application/x-tar" && media!="application/octet-stream") || len(params)!=0 {
            sendError(w,http.StatusUnsupportedMediaType,"invalid_content_type","A raw tar package is required")
            return
        }
        if r.ContentLength>deploypackage.MaxArchiveBytes {
            sendError(w,http.StatusRequestEntityTooLarge,"package_too_large","Package exceeds maximum size")
            return
        }
        digest:=strings.TrimSpace(r.Header.Get("X-EHA-Package-SHA256"))
        if digest=="" || len(digest)!=64 || strings.ContainsAny(digest," \t\r\n") {
            sendError(w,http.StatusBadRequest,"invalid_digest","Pinned package SHA-256 header required")
            return
        }
        limited:=http.MaxBytesReader(w,r.Body,deploypackage.MaxArchiveBytes+1)
        defer limited.Close()
        receipt,err:=packages.Put(r.Context(),limited,digest)
        if err!=nil {
            var maxErr *http.MaxBytesError
            switch {
            case errors.Is(err,deploypackage.ErrTooLarge),errors.As(err,&maxErr):
                sendError(w,http.StatusRequestEntityTooLarge,"package_too_large","Package exceeds maximum size")
            case errors.Is(err,deploypackage.ErrInvalid):
                sendError(w,http.StatusUnprocessableEntity,"invalid_deploy_package","Package integrity or contract verification failed")
            case errors.Is(err,deploypackage.ErrConflict):
                sendError(w,http.StatusConflict,"package_conflict","Pinned package already exists with different content")
            default:
                sendError(w,http.StatusServiceUnavailable,"package_unavailable","Could not safely stage package")
            }
            return
        }
        if !receipt.Reused {w.WriteHeader(http.StatusCreated)}
        sendJSON(w,receipt)
    })))

    mux.Handle("/v1/deploy/packages/{app_id}/{git_revision}",auth.RequireController(agentID,pairingStore,http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
        if !onlyGET(w,r) {return}
        receipt,err:=packages.Lookup(r.PathValue("app_id"),r.PathValue("git_revision"))
        if err!=nil {
            switch {
            case errors.Is(err,deploypackage.ErrNotFound):
                sendError(w,http.StatusNotFound,"package_not_found","No stored package for this revision")
            case errors.Is(err,deploypackage.ErrInvalid):
                sendError(w,http.StatusBadRequest,"invalid_package_key","Invalid package key")
            case errors.Is(err,deploypackage.ErrConflict):
                sendError(w,http.StatusConflict,"package_integrity_failed","Stored package integrity check failed")
            default:
                sendError(w,http.StatusServiceUnavailable,"package_unavailable","Package store is unavailable")
            }
            return
        }
        sendJSON(w,receipt)
    })))
}
