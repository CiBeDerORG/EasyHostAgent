package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockertasks"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

// registerDockerTasks 仅开放限定的持久 Docker 资源任务，禁止任意命令执行。
func registerDockerTasks(mux *http.ServeMux, agentID string, store *pairing.Store, tasks *dockertasks.Service) {
	mux.Handle("/v1/tasks", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			limit := 50
			if raw := r.URL.Query().Get("limit"); raw != "" {
				number, err := strconv.Atoi(raw)
				if err != nil || number < 1 || number > 100 {
					sendError(w, http.StatusBadRequest, "invalid_limit", "limit 必须为 1-100")
					return
				}
				limit = number
			}
			sendJSON(w, map[string]any{"agent_id": agentID, "tasks": tasks.List(limit)})
		case http.MethodPost:
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "application/json" {
				sendError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "请求正文必须为 JSON")
				return
			}
			var request dockertasks.Request
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				sendError(w, http.StatusBadRequest, "invalid_task", "无效的任务请求")
				return
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				sendError(w, http.StatusBadRequest, "invalid_task", "只允许一个 JSON 对象")
				return
			}
			task, err := tasks.Submit(request)
			if err != nil {
				switch {
				case errors.Is(err, dockertasks.ErrInvalid):
					sendError(w, http.StatusUnprocessableEntity, "unsupported_action", "仅支持 Docker 资源 start、stop、restart")
				case errors.Is(err, dockertasks.ErrConflict):
					sendError(w, http.StatusConflict, "task_conflict", "任务 ID 或资源存在冲突")
				case errors.Is(err, dockertasks.ErrFull):
					sendError(w, http.StatusServiceUnavailable, "task_capacity", "任务历史记录已达到上限")
				default:
					sendError(w, http.StatusServiceUnavailable, "task_storage", "任务无法持久化")
				}
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(task)
		default:
			w.Header().Set("Allow", "GET, POST")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持的请求方法")
		}
	})))
	mux.Handle("/v1/tasks/{task_id}", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		task, err := tasks.Get(r.PathValue("task_id"))
		if err != nil {
			sendError(w, http.StatusNotFound, "task_not_found", "Docker 任务不存在")
			return
		}
		sendJSON(w, task)
	})))
	mux.Handle("/v1/tasks/{task_id}/cancel", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持的请求方法")
			return
		}
		task, err := tasks.Cancel(r.PathValue("task_id"))
		if err != nil {
			sendError(w, http.StatusNotFound, "task_not_found", "Docker 任务不存在")
			return
		}
		sendJSON(w, task)
	})))
}
