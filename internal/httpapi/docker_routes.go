package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/auth"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

func registerDocker(mux *http.ServeMux, agentID string, store *pairing.Store, engine *dockerengine.Client) {
	mux.Handle("/v1/docker/inventory", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		snapshot, err := engine.InspectGrouped(r.Context())
		resources := []map[string]any{}
		if err == nil {
			for _, resource := range snapshot.Resources {
				resources = append(resources, formatDockerResource(resource))
			}
		}
		payload := map[string]any{
			"agent_id": agentID, "observed_at": snapshot.ObservedAt,
			"complete": err == nil && snapshot.Complete,
			"status": "ok", "resources": resources,
		}
		if err != nil {
			payload["status"] = "error"
			payload["error"] = dockerengine.PublicError(err)
		}
		sendJSON(w, payload)
	})))
	mux.Handle("/v1/docker/resources/{resource_id}/logs", auth.RequireController(agentID, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGET(w, r) {
			return
		}
		id := r.PathValue("resource_id")
		if len(id) == 0 || len(id) > 256 {
			sendError(w, http.StatusBadRequest, "invalid_resource", "Invalid resource identifier")
			return
		}
		tail := 100
		if raw := r.URL.Query().Get("tail"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				sendError(w, http.StatusBadRequest, "invalid_tail", "tail must be from 1 to 500")
				return
			}
			tail = parsed
		}
		logs, err := engine.ReadLogs(r.Context(), id, tail)
		if err != nil {
			switch {
			case errors.Is(err, dockerengine.ErrResourceNotFound):
				sendError(w, http.StatusNotFound, "resource_not_found", "Docker resource not found")
			case errors.Is(err, dockerengine.ErrProtocol):
				sendError(w, http.StatusBadRequest, "invalid_docker_request", dockerengine.PublicError(err))
			default:
				sendError(w, http.StatusServiceUnavailable, "docker_unavailable", dockerengine.PublicError(err))
			}
			return
		}
		sendJSON(w, map[string]any{
			"agent_id": agentID, "resource_id": id,
			"text": logs.Text, "truncated": logs.Truncated,
		})
	})))
}

func formatDockerResource(r dockerengine.Resource) map[string]any {
	kind := "container"
	if r.Kind == dockerengine.KindCompose {
		kind = "compose_project"
	}
	containers := []map[string]any{}
	ports := []map[string]any{}
	for _, c := range r.Containers {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		state := c.State
		switch state {
		case "running", "created", "restarting", "paused", "exited", "dead":
		default:
			state = "unknown"
		}
		ownPorts := []map[string]any{}
		for _, p := range c.Ports {
			if p.PublicPort == 0 || p.PrivatePort == 0 || p.Type != "tcp" && p.Type != "udp" {
				continue
			}
			entry := map[string]any{
				"protocol": p.Type, "host_ip": p.IP, "host_port": p.PublicPort,
				"container_port": p.PrivatePort, "is_web": false,
			}
			ownPorts = append(ownPorts, entry)
			ports = append(ports, entry)
		}
		containers = append(containers, map[string]any{
			"container_id": c.ID, "name": name,
			"image": c.Image, "state": state, "ports": ownPorts,
		})
	}
	item := map[string]any{
		"resource_id": r.ID, "kind": kind, "name": r.Name,
		"state": r.Status, "containers": containers,
		"managed": false, "ports": ports,
	}
	if r.Kind == dockerengine.KindCompose {
		item["compose_project"] = r.ComposeProject
	}
	return item
}
