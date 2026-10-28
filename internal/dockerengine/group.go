package dockerengine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrIncomplete prevents consumers from interpreting a partial Docker scan
// as evidence that previously known containers or applications disappeared.
var ErrIncomplete = errors.New("docker inventory incomplete")

const (
	KindCompose   = "compose"
	KindContainer = "container"
)

// Resource is one Compose project or one container without Compose labels.
// A Docker host is the scope: callers must never merge resources across hosts.
type Resource struct {
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Name           string      `json:"name"`
	ComposeProject string      `json:"compose_project,omitempty"`
	Services       []string    `json:"services"`
	Status         string      `json:"status"`
	ContainerCount int         `json:"container_count"`
	RunningCount   int         `json:"running_count"`
	Containers     []Container `json:"containers"`
}

// GroupedInventory is authoritative only if Complete is true. Every error
// returns an empty, incomplete snapshot, never a misleading partial list.
type GroupedInventory struct {
	ObservedAt string     `json:"observed_at"`
	Complete   bool       `json:"complete"`
	Resources  []Resource `json:"resources"`
}

// Group collects containers with the same com.docker.compose.project label.
// No label means standalone, regardless of similar image or container name.
func Group(snapshot Inventory) (GroupedInventory, error) {
	out := GroupedInventory{
		ObservedAt: snapshot.ObservedAt,
		Resources:  []Resource{},
	}
	if !snapshot.Complete {
		return out, ErrIncomplete
	}

	groups := make(map[string]*Resource, len(snapshot.Containers))
	seen := make(map[string]struct{}, len(snapshot.Containers))
	for _, container := range snapshot.Containers {
		if strings.TrimSpace(container.ID) == "" {
			return out, fmt.Errorf("%w: container ID missing", ErrProtocol)
		}
		if _, exists := seen[container.ID]; exists {
			return out, fmt.Errorf("%w: duplicate container ID", ErrProtocol)
		}
		seen[container.ID] = struct{}{}

		project := strings.TrimSpace(container.Labels["com.docker.compose.project"])
		kind, key, name := KindContainer, "container:"+container.ID, container.ID
		if len(container.Names) != 0 && strings.TrimSpace(container.Names[0]) != "" {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		if project != "" {
			kind, key, name = KindCompose, "compose:"+project, project
		}
		res := groups[key]
		if res == nil {
			res = &Resource{
				ID: key, Kind: kind, Name: name, ComposeProject: project,
				Services: []string{}, Containers: []Container{},
			}
			groups[key] = res
		}
		res.Containers = append(res.Containers, container)
		res.ContainerCount++
		if container.State == "running" {
			res.RunningCount++
		}
		if kind == KindCompose {
			service := strings.TrimSpace(container.Labels["com.docker.compose.service"])
			if service != "" {
				present := false
				for _, old := range res.Services {
					if old == service {
						present = true
						break
					}
				}
				if !present {
					res.Services = append(res.Services, service)
				}
			}
		}
	}
	for _, resource := range groups {
		sort.Strings(resource.Services)
		sort.Slice(resource.Containers, func(i, j int) bool {
			return resource.Containers[i].ID < resource.Containers[j].ID
		})
		resource.Status = summarizeState(resource.Containers)
		out.Resources = append(out.Resources, *resource)
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		return out.Resources[i].ID < out.Resources[j].ID
	})
	out.Complete = true
	return out, nil
}

func summarizeState(containers []Container) string {
	if len(containers) == 0 {
		return "unknown"
	}
	running, stopped := 0, 0
	for _, container := range containers {
		switch container.State {
		case "running":
			running++
		case "exited", "created", "dead":
			stopped++
		}
	}
	switch {
	case running == len(containers):
		return "running"
	case stopped == len(containers):
		return "stopped"
	case running > 0:
		return "partial"
	default:
		return "unknown"
	}
}
