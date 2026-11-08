package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/CiBeDerORG/EasyHostAgent/internal/adapters"
	"github.com/CiBeDerORG/EasyHostAgent/internal/adapters/easytier"
	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

// Adapter API 全部使用既有配对 Bearer 鉴权；不暴露任意容器命令接口。
func registerAdapters(mux *http.ServeMux, agentID string, store *pairing.Store, drivers *adapters.Service) {
	mux.Handle("GET /v1/adapters/types", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, map[string]any{"agent_id": agentID, "types": drivers.Types()})
	})))
	mux.Handle("PUT /v1/adapters/types/{type}", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if r.Header.Get("Content-Type") != "application/json" || decoder.Decode(&input) != nil || input.Enabled == nil || decoder.Decode(new(any)) != io.EOF {
			sendError(w, 400, "invalid_request", "Driver 开关参数无效")
			return
		}
		if err := drivers.Set(r.PathValue("type"), *input.Enabled); err != nil {
			if errors.Is(err, adapters.ErrUnknown) {
				sendError(w, 404, "unknown_driver", "Driver 类型不存在")
			} else {
				sendError(w, 503, "driver_settings_unavailable", "Driver 设置保存失败")
			}
			return
		}
		sendJSON(w, map[string]any{"agent_id": agentID, "types": drivers.Types()})
	})))
	// Fixed EasyTier port-forward routes; never accept command or container IDs
	// from callers. All three methods reuse paired-controller authorization.
	mux.Handle("GET /v1/adapters/easytier/port-forwards", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) != 0 {
			for key := range q {
				if key != "resource_id" && key != "instance_id" && key != "instance_name" {
					sendError(w, 400, "invalid_request", "端口转发查询参数无效")
					return
				}
				if len(q[key]) != 1 {
					sendError(w, 400, "invalid_request", "查询参数重复")
					return
				}
			}
		}
		selector := adapters.InstanceSelector{InstanceID: q.Get("instance_id"), InstanceName: q.Get("instance_name")}
		data, err := drivers.PortForwards(r.Context(), q.Get("resource_id"), selector)
		if err != nil {
			sendPortForwardError(w, err)
			return
		}
		sendJSON(w, map[string]any{"agent_id": agentID, "adapter_type": "easytier", "resource_id": q.Get("resource_id"), "data": json.RawMessage(data)})
	})))
	mux.Handle("POST /v1/adapters/easytier/port-forwards", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changePortForward(w, r, agentID, drivers, "add")
	})))
	mux.Handle("DELETE /v1/adapters/easytier/port-forwards", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changePortForward(w, r, agentID, drivers, "remove")
	})))
	mux.Handle("GET /v1/adapters/{type}/{capability}", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resourceID := r.URL.Query().Get("resource_id")
		if len(resourceID) == 0 || len(resourceID) > 256 {
			sendError(w, 400, "invalid_resource", "必须指定现有 Docker 资源 ID")
			return
		}
		kind := r.PathValue("type")
		capability := r.PathValue("capability")
		data, err := drivers.Read(r.Context(), kind, resourceID, capability)
		if err != nil {
			switch {
			case errors.Is(err, adapters.ErrUnknown):
				sendError(w, 404, "unknown_driver", "Driver 不存在")
			case errors.Is(err, adapters.ErrDisabled):
				sendError(w, 403, "driver_disabled", "该 Driver 已关闭")
			default:
				sendError(w, 422, "driver_read_failed", "Driver 查询失败，请检查容器、CLI 或 RPC 状态")
			}
			return
		}
		sendJSON(w, map[string]any{
			"agent_id": agentID, "adapter_type": kind, "resource_id": resourceID,
			"capability": capability, "data": json.RawMessage(data),
		})
	})))
}

func changePortForward(w http.ResponseWriter, r *http.Request, agentID string, drivers *adapters.Service, action string) {
	if r.Header.Get("Content-Type") != "application/json" {
		sendError(w, 415, "content_type", "需要 JSON")
		return
	}
	var req adapters.PortForwardRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		sendError(w, 400, "invalid_request", "端口转发请求参数无效")
		return
	}
	if err := drivers.ModifyPortForward(r.Context(), req, action); err != nil {
		sendPortForwardError(w, err)
		return
	}
	sendJSON(w, map[string]any{
		"agent_id": agentID, "adapter_type": "easytier",
		"resource_id": req.ResourceID, "instance_id": req.InstanceID, "instance_name": req.InstanceName,
		"action": action, "applied": true,
	})
}

func sendPortForwardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adapters.ErrDisabled):
		sendError(w, 403, "driver_disabled", "EasyTier Driver 未启用")
	case errors.Is(err, easytier.ErrInvalidPortForward):
		sendError(w, 400, "invalid_port_forward", "端口转发参数无效或需要显式确认公网监听")
	default:
		sendError(w, 422, "port_forward_failed", "EasyTier 端口转发操作失败，请检查官方 Core/RPC 和目标网络实例")
	}
}
