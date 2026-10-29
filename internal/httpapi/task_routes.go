package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/taskstore"
)

// registerTasks exposes durable task history only. Submission and execution
// remain disabled until the allowlisted runner is wired in T04-03.
func registerTasks(mux *http.ServeMux, agentID string, pairingStore *pairing.Store, tasks *taskstore.Store) {
	mux.Handle("/v1/tasks", auth.RequireController(agentID, pairingStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Task history is read-only until task execution is enabled")
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				sendError(w, http.StatusBadRequest, "invalid_task_limit", "Task list limit must be between 1 and 100")
				return
			}
			limit = parsed
		}
		page, err := tasks.List(limit, r.URL.Query().Get("cursor"))
		if err != nil {
			sendTaskStoreError(w, err)
			return
		}
		sendJSON(w, page)
	})))

	mux.Handle("/v1/tasks/{task_id}", auth.RequireController(agentID, pairingStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Task state is read-only until task execution is enabled")
			return
		}
		task, err := tasks.Get(r.PathValue("task_id"))
		if err != nil {
			sendTaskStoreError(w, err)
			return
		}
		sendJSON(w, task)
	})))

	mux.Handle("/v1/tasks/{task_id}/logs", auth.RequireController(agentID, pairingStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Task logs are read-only until task execution is enabled")
			return
		}
		offset := int64(0)
		if raw := r.URL.Query().Get("offset"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 {
				sendError(w, http.StatusBadRequest, "invalid_log_offset", "Log offset must be a non-negative byte cursor")
				return
			}
			offset = parsed
		}
		chunk, err := tasks.ReadLogs(r.PathValue("task_id"), offset, taskstore.MaxLogChunkBytes)
		if err != nil {
			sendTaskStoreError(w, err)
			return
		}
		sendJSON(w, chunk)
	})))
}

func sendTaskStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, taskstore.ErrNotFound):
		sendError(w, http.StatusNotFound, "task_not_found", "Task was not found")
	case errors.Is(err, taskstore.ErrInvalidRequest), errors.Is(err, taskstore.ErrInvalidOffset):
		sendError(w, http.StatusBadRequest, "invalid_task_query", "Task query is invalid")
	default:
		sendError(w, http.StatusServiceUnavailable, "task_store_unavailable", "Task state is temporarily unavailable")
	}
}
