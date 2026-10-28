// Package dockerengine reads Docker Engine over its local Unix socket.
// Inventory is captured only on explicit calls, never in a background poller.
package dockerengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("docker engine unavailable")
	ErrPermission  = errors.New("docker socket access denied")
	ErrProtocol    = errors.New("unsupported docker engine response")
)

const maxJSONBytes int64 = 8 << 20

// Client never uses DOCKER_HOST, remote TCP or HTTP proxy settings.
type Client struct {
	socket string
	http   *http.Client
}

type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
}

type Port struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source,omitempty"`
	Destination string `json:"Destination"`
	Driver      string `json:"Driver,omitempty"`
	Mode        string `json:"Mode,omitempty"`
	RW          bool   `json:"RW"`
}

type Network struct {
	NetworkID  string `json:"NetworkID,omitempty"`
	IPAddress  string `json:"IPAddress,omitempty"`
	MacAddress string `json:"MacAddress,omitempty"`
}

type Container struct {
	ID              string             `json:"Id"`
	Names           []string           `json:"Names"`
	Image           string             `json:"Image"`
	ImageID         string             `json:"ImageID"`
	Command         string             `json:"Command"`
	Created         int64              `json:"Created"`
	State           string             `json:"State"`
	Status          string             `json:"Status"`
	Labels          map[string]string  `json:"Labels"`
	Ports           []Port             `json:"Ports"`
	Mounts          []Mount            `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]Network `json:"Networks"`
	} `json:"NetworkSettings"`
}

type Image struct {
	ID          string            `json:"Id"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	Labels      map[string]string `json:"Labels"`
}

type Inventory struct {
	ObservedAt string      `json:"observed_at"`
	Complete   bool        `json:"complete"`
	Version    Version     `json:"version"`
	APIVersion string      `json:"api_version"`
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Error      string      `json:"error,omitempty"`
}

// New uses the local Docker Engine socket. The path may be overridden by
// tests or a future validated host setting; the environment is never trusted.
func New(socketPath string) (*Client, error) {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || socketPath == "/" {
		return nil, fmt.Errorf("%w: socket path must be absolute and normalized", ErrProtocol)
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{
		socket: socketPath,
		http: &http.Client{
			Transport: tr,
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Close() {
	c.http.CloseIdleConnections()
}

func (c *Client) read(ctx context.Context, path string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker.local"+path, nil)
	if err != nil {
		return ErrProtocol
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return ErrPermission
		}
		return fmt.Errorf("%w: cannot reach Unix socket", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrPermission
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes+1))
	if err != nil || int64(len(data)) > maxJSONBytes {
		return fmt.Errorf("%w: unreadable or oversized JSON", ErrProtocol)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("%w: malformed JSON", ErrProtocol)
	}
	return nil
}

func parseAPI(value string) (int, int, error) {
	major, minor, ok := strings.Cut(value, ".")
	if !ok || major == "" || minor == "" || strings.Contains(minor, ".") {
		return 0, 0, ErrProtocol
	}
	m, a := strconv.Atoi(major)
	n, b := strconv.Atoi(minor)
	if a != nil || b != nil || m != 1 || n < 0 || n > 999 {
		return 0, 0, ErrProtocol
	}
	return m, n, nil
}

// selectAPI chooses a stable supported version while honoring the server's
// advertised minimum. Docker Engine API v1.24 is the oldest supported here.
func selectAPI(version Version) (string, error) {
	_, upper, err := parseAPI(version.APIVersion)
	if err != nil {
		return "", err
	}
	lower := 24
	if version.MinAPIVersion != "" {
		_, minVersion, err := parseAPI(version.MinAPIVersion)
		if err != nil {
			return "", err
		}
		if minVersion > lower {
			lower = minVersion
		}
	}
	if upper < lower {
		return "", ErrProtocol
	}
	chosen := 41
	if chosen < lower {
		chosen = lower
	}
	if chosen > upper {
		chosen = upper
	}
	return fmt.Sprintf("1.%d", chosen), nil
}

// Inspect lists stopped AND running containers and all images. A partial
// response always has Complete=false; callers must NEVER infer deletions.
func (c *Client) Inspect(ctx context.Context) (Inventory, error) {
	out := Inventory{
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
		Containers: []Container{},
		Images:     []Image{},
	}
	fail := func(err error) (Inventory, error) {
		out.Error = err.Error()
		return out, err
	}
	if err := c.read(ctx, "/version", &out.Version); err != nil {
		return fail(err)
	}
	api, err := selectAPI(out.Version)
	if err != nil {
		return fail(err)
	}
	out.APIVersion = api
	if err := c.read(ctx, "/v"+api+"/containers/json?all=1", &out.Containers); err != nil {
		return fail(err)
	}
	if out.Containers == nil {
		out.Containers = []Container{}
	}
	if err := c.read(ctx, "/v"+api+"/images/json?all=1", &out.Images); err != nil {
		return fail(err)
	}
	if out.Images == nil {
		out.Images = []Image{}
	}
	out.Complete = true
	return out, nil
}
