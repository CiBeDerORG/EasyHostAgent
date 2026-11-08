package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/CiBeDerORG/EasyHostAgent/internal/adapters"
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
