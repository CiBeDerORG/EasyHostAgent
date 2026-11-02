package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
	"github.com/CiBeDerORG/EasyHostAgent/internal/tasks"
	"github.com/CiBeDerORG/EasyHostAgent/internal/trust"
)

// registerTasks 仅对已配对控制中心开放“准备/查看/取消”；无 worker，无执行入口。
func registerTasks(mux *http.ServeMux, agentID string, grants *pairing.Store, store *tasks.Store) {
	readAuth := func(r *http.Request) (string, string) {
		return r.Header.Get("X-EHA-Controller-ID"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, tasks.ErrUnauthorized):
			sendError(w, http.StatusUnauthorized, "controller_changed", "当前控制中心授权已失效")
		case errors.Is(err, tasks.ErrInvalid):
			sendError(w, http.StatusBadRequest, "invalid_task", "部署意图参数无效")
		case errors.Is(err, tasks.ErrConflict):
			sendError(w, http.StatusConflict, "resource_reserved", "幂等键冲突或应用已被其他准备任务预留")
		case errors.Is(err, tasks.ErrNotFound):
			sendError(w, http.StatusNotFound, "task_not_found", "部署任务不存在")
		case errors.Is(err, trust.ErrNotApproved):
			sendError(w, http.StatusForbidden, "bundle_not_approved", "ZIP 未经主机本地信任批准")
		default:
			sendError(w, http.StatusServiceUnavailable, "task_unavailable", "部署任务存储或信任状态不可用")
		}
	}
	mux.Handle("/v1/tasks", auth.RequireController(agentID, grants, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller, token := readAuth(r)
		switch r.Method {
		case http.MethodGet:
			items, err := store.List(controller, token)
			if err != nil {
				fail(w, err)
				return
			}
			sendJSON(w, map[string]any{"agent_id": agentID, "tasks": items, "execution_enabled": false})
		case http.MethodPost:
			typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || typ != "application/json" {
				sendError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "请求必须为 JSON")
				return
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			var req tasks.Input
			if err := decoder.Decode(&req); err != nil {
				sendError(w, http.StatusBadRequest, "invalid_task", "请求 JSON 不合法")
				return
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				sendError(w, http.StatusBadRequest, "invalid_task", "仅允许一个请求 JSON 对象")
				return
			}
			task, repeated, err := store.Submit(controller, token, req)
			if err != nil {
				fail(w, err)
				return
			}
			status := http.StatusCreated
			if repeated {
				status = http.StatusOK
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"agent_id": agentID, "task": task, "idempotent_replay": repeated, "execution_enabled": false,
			})
		default:
			w.Header().Set("Allow", "GET, POST")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅允许查询与准备任务")
		}
	})))
	mux.Handle("/v1/tasks/{id}", auth.RequireController(agentID, grants, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller, token := readAuth(r)
		id := r.PathValue("id")
		switch r.Method {
		case http.MethodGet:
			task, err := store.Get(controller, token, id)
			if err != nil {
				fail(w, err)
				return
			}
			sendJSON(w, map[string]any{"agent_id": agentID, "task": task, "execution_enabled": false})
		case http.MethodDelete:
			task, err := store.Cancel(controller, token, id)
			if err != nil {
				fail(w, err)
				return
			}
			sendJSON(w, map[string]any{"agent_id": agentID, "task": task, "execution_enabled": false})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			sendError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持启动部署任务")
		}
	})))
}
