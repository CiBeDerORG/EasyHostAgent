package dockerengine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const MaxLogBytes = 256 << 10

type Logs struct {
	ResourceID string `json:"resource_id"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
}

func parseDockerLog(data []byte) string {
	if len(data) < 8 || (data[0] != 1 && data[0] != 2 && data[0] != 3) ||
		data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return strings.ToValidUTF8(string(data), "�")
	}
	var out strings.Builder
	for len(data) > 0 {
		if len(data) < 8 || (data[0] != 1 && data[0] != 2 && data[0] != 3) ||
			data[1] != 0 || data[2] != 0 || data[3] != 0 {
			// A short final frame is expected when a response is truncated.
			break
		}
		n := int(binary.BigEndian.Uint32(data[4:8]))
		data = data[8:]
		if n > len(data) {
			n = len(data)
		}
		out.WriteString(strings.ToValidUTF8(string(data[:n]), "�"))
		data = data[n:]
	}
	return out.String()
}

func (c *Client) readContainerLogs(ctx context.Context, api, id string, tail, limit int) (string, bool, error) {
	if !validContainerID(id) {
		return "", false, ErrProtocol
	}
	query := "?stdout=1&stderr=1&timestamps=1&follow=0&tail=" + strconv.Itoa(tail)
	endpoint := "http://docker.local/v" + api + "/containers/" + url.PathEscape(id) + "/logs" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, ErrProtocol
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("%w: Docker log request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, ErrResourceNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", false, ErrPermission
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("%w: Docker logs HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", false, fmt.Errorf("%w: cannot read Docker logs", ErrUnavailable)
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	return parseDockerLog(data), truncated, nil
}

// ReadLogs returns a fixed-size, non-following log snippet for a resource
// resolved from a fresh complete inventory. It does not open a stream.
func (c *Client) ReadLogs(ctx context.Context, resourceID string, tail int) (Logs, error) {
	result := Logs{ResourceID: resourceID}
	if tail < 1 || tail > 500 {
		return result, ErrProtocol
	}
	resource, api, err := c.findResource(ctx, resourceID)
	if err != nil {
		return result, err
	}
	var out strings.Builder
	for i, container := range resource.Containers {
		header := fmt.Sprintf("[%s] %s\n", container.ID, strings.Join(container.Names, ","))
		remaining := MaxLogBytes - out.Len()
		if remaining <= len(header) {
			result.Truncated = true
			break
		}
		out.WriteString(header)
		remaining = MaxLogBytes - out.Len()
		chunk, truncated, err := c.readContainerLogs(ctx, api, container.ID, tail, remaining)
		if err != nil {
			return result, err
		}
		out.WriteString(chunk)
		if truncated || i < len(resource.Containers)-1 && out.Len() >= MaxLogBytes {
			result.Truncated = true
			break
		}
	}
	result.Text = out.String()
	return result, nil
}

// Runtime API errors must be sanitized before returning them to a browser.
func PublicError(err error) string {
	switch {
	case errors.Is(err, ErrIncomplete):
		return "Docker inventory is incomplete"
	case errors.Is(err, ErrPermission):
		return "Docker Engine access denied"
	case errors.Is(err, ErrResourceNotFound):
		return "Docker resource no longer exists"
	case errors.Is(err, ErrProtocol):
		return "Docker response or request invalid"
	default:
		return "Docker Engine unavailable"
	}
}
