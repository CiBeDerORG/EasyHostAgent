package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrResourceNotFound = errors.New("docker resource not found in complete inventory")
	ErrInvalidAction    = errors.New("unsupported Docker resource action")
	ErrPartialOperation = errors.New("Docker operation partially completed or verification failed")
)

type ActionResult struct {
	ResourceID  string   `json:"resource_id"`
	Action      string   `json:"action"`
	Attempted   []string `json:"attempted_container_ids"`
	Succeeded   []string `json:"succeeded_container_ids"`
	Verified    bool     `json:"verified"`
	FinalStatus string   `json:"final_status,omitempty"`
}

// findResource resolves an exact opaque ID from a fresh complete inventory.
// Client-provided text is never interpolated into a Docker operation path.
func (c *Client) findResource(ctx context.Context, resourceID string) (Resource, string, error) {
	if len(resourceID) == 0 || len(resourceID) > 256 {
		return Resource{}, "", ErrResourceNotFound
	}
	snapshot, err := c.Inspect(ctx)
	if err != nil {
		return Resource{}, "", err
	}
	grouped, err := Group(snapshot)
	if err != nil {
		return Resource{}, "", err
	}
	for _, item := range grouped.Resources {
		if item.ID == resourceID {
			return item, snapshot.APIVersion, nil
		}
	}
	return Resource{}, "", ErrResourceNotFound
}

func validContainerID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func (c *Client) operateContainer(ctx context.Context, api, containerID, action string) error {
	if !validContainerID(containerID) {
		return ErrProtocol
	}
	path := fmt.Sprintf("http://docker.local/v%s/containers/%s/%s", api, url.PathEscape(containerID), action)
	if action == "stop" || action == "restart" {
		path += "?t=10"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return ErrProtocol
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: Docker action unreachable", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusNotModified && (action == "start" || action == "stop") {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrResourceNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrPermission
	}
	return fmt.Errorf("%w: action HTTP %d", ErrUnavailable, resp.StatusCode)
}

// Act applies a narrow allowlist to every container of a resolved resource.
// The caller MUST be the authenticated, persistent task executor (T04-01/02).
// It is intentionally not an HTTP mutation endpoint: no bypass around task
// idempotency, audit, or application-scoped locks is allowed.
func (c *Client) Act(ctx context.Context, resourceID, action string) (ActionResult, error) {
	result := ActionResult{ResourceID: resourceID, Action: action,
		Attempted: []string{}, Succeeded: []string{}}
	if action != "start" && action != "stop" && action != "restart" {
		return result, ErrInvalidAction
	}
	resource, api, err := c.findResource(ctx, resourceID)
	if err != nil {
		return result, err
	}
	// Re-resolve the resource before every operation. Compose contains stopped
	// containers and acts on ALL members, never a single arbitrary service.
	for _, container := range resource.Containers {
		result.Attempted = append(result.Attempted, container.ID)
		if err := c.operateContainer(ctx, api, container.ID, action); err != nil {
			return result, errors.Join(ErrPartialOperation, err)
		}
		result.Succeeded = append(result.Succeeded, container.ID)
	}
	after, _, err := c.findResource(ctx, resourceID)
	if err != nil {
		return result, errors.Join(ErrPartialOperation, err)
	}
	result.FinalStatus = after.Status
	if len(after.Containers) != len(resource.Containers) {
		return result, ErrPartialOperation
	}
	beforeIDs := make(map[string]struct{}, len(resource.Containers))
	for _, container := range resource.Containers {
		beforeIDs[container.ID] = struct{}{}
	}
	for _, container := range after.Containers {
		if _, ok := beforeIDs[container.ID]; !ok {
			return result, ErrPartialOperation
		}
		if action == "stop" {
			if container.State != "exited" && container.State != "created" {
				return result, ErrPartialOperation
			}
		} else if container.State != "running" {
			return result, ErrPartialOperation
		}
	}
	result.Verified = true
	return result, nil
}

// ActionDescription documents what the supported Docker operations do.
// Compose operations act on existing containers only: they cannot perform a
// Compose recreate, dependency resolution, image pull, or service rollout.
func ActionDescription(action string) string {
	switch strings.ToLower(action) {
	case "start":
		return "Start all existing containers in the selected resource"
	case "stop":
		return "Stop all existing containers in the selected resource"
	case "restart":
		return "Restart all existing containers in the selected resource"
	default:
		return ""
	}
}
