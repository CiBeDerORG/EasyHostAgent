package easytier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrInvalidPortForward = errors.New("invalid port-forward request")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Instance selectors refer to EasyTier's own network instances, not Driver
// instances. No adapter ID is generated or persisted.
type InstanceSelector struct {
	InstanceID   string `json:"instance_id,omitempty"`
	InstanceName string `json:"instance_name,omitempty"`
}

type PortForwardRequest struct {
	ResourceID      string `json:"resource_id"`
	InstanceID      string `json:"instance_id,omitempty"`
	InstanceName    string `json:"instance_name,omitempty"`
	Protocol        string `json:"protocol"`
	Listen          string `json:"listen"`
	Destination     string `json:"destination,omitempty"`
	AllowPublicBind bool   `json:"allow_public_bind,omitempty"`
}

// selectorArgs always provides a single explicit network target for writes.
// Read-only listing may list all instances.
func selectorArgs(selector InstanceSelector, requireTarget bool) ([]string, error) {
	if selector.InstanceID != "" && selector.InstanceName != "" {
		return nil, ErrInvalidPortForward
	}
	if selector.InstanceID != "" {
		if !uuidPattern.MatchString(selector.InstanceID) { return nil, ErrInvalidPortForward }
		return []string{"--instance-id", selector.InstanceID}, nil
	}
	if selector.InstanceName != "" {
		if len(selector.InstanceName) > 128 || strings.TrimSpace(selector.InstanceName) != selector.InstanceName ||
			strings.HasPrefix(selector.InstanceName, "-") || strings.ContainsAny(selector.InstanceName, "\r\n\x00") {
			return nil, ErrInvalidPortForward
		}
		return []string{"--instance-name", selector.InstanceName}, nil
	}
	if requireTarget { return nil, ErrInvalidPortForward }
	return nil, nil
}

func socketAddress(value string, isListener bool) (string, bool, error) {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return "", false, ErrInvalidPortForward
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil { return "", false, ErrInvalidPortForward }
	ip := net.ParseIP(host)
	if ip == nil || ip.IsMulticast() { return "", false, ErrInvalidPortForward }
	if !isListener && (ip.IsUnspecified() || ip.Equal(net.IPv4bcast)) {
		return "", false, ErrInvalidPortForward
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 { return "", false, ErrInvalidPortForward }
	return net.JoinHostPort(ip.String(), strconv.Itoa(number)), ip.IsUnspecified(), nil
}

func portForwardArgs(action string, req PortForwardRequest) ([]string, error) {
	if req.ResourceID != "compose:easytier" { return nil, ErrInvalidPortForward }
	args, err := selectorArgs(InstanceSelector{InstanceID: req.InstanceID, InstanceName: req.InstanceName}, true)
	if err != nil { return nil, err }
	if action != "add" && action != "remove" { return nil, ErrInvalidPortForward }
	if req.Protocol != "tcp" && req.Protocol != "udp" { return nil, ErrInvalidPortForward }
	listen, exposed, err := socketAddress(req.Listen, true)
	if err != nil { return nil, err }
	if action == "add" && exposed && !req.AllowPublicBind { return nil, ErrInvalidPortForward }
	args = append(args, "port-forward", action, req.Protocol, listen)
	if action == "add" && req.Destination == "" { return nil, ErrInvalidPortForward }
	if req.Destination != "" {
		destination, _, err := socketAddress(req.Destination, false)
		if err != nil { return nil, err }
		args = append(args, destination)
	}
	return args, nil
}

// mutate serializes modifications on a local Agent to avoid overlapping CLI
// patches. Changes are stored by the official EasyTier Core/Config Server.
var mutationMu sync.Mutex

func (d *Driver) ListPortForwards(ctx context.Context, resourceID string, selector InstanceSelector) (json.RawMessage, error) {
	if resourceID != "compose:easytier" { return nil, ErrInvalidPortForward }
	args, err := selectorArgs(selector, false)
	if err != nil { return nil, err }
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	containerID, err := d.findContainer(ctx, resourceID)
	if err != nil { return nil, err }
	rpc, err := d.engine.EasyTierRPCAddress(ctx, containerID)
	if err != nil { return nil, errors.New("EasyTier RPC 端口不可用") }
	argv := append([]string{"easytier-cli", "-p", rpc, "-o", "json"}, args...)
	argv = append(argv, "port-forward", "list")
	output, err := d.engine.Exec(ctx, containerID, argv)
	if err != nil { return nil, fmt.Errorf("EasyTier 端口转发查询失败: %w", err) }
	if !json.Valid(output) { return nil, errors.New("EasyTier 端口转发结果不是有效 JSON") }
	return json.RawMessage(output), nil
}

func (d *Driver) ModifyPortForward(ctx context.Context, req PortForwardRequest, action string) error {
	args, err := portForwardArgs(action, req)
	if err != nil { return err }
	mutationMu.Lock()
	defer mutationMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	containerID, err := d.findContainer(ctx, req.ResourceID)
	if err != nil { return err }
	rpc, err := d.engine.EasyTierRPCAddress(ctx, containerID)
	if err != nil { return errors.New("EasyTier RPC 端口不可用") }
	argv := append([]string{"easytier-cli", "-p", rpc}, args...)
	_, err = d.engine.Exec(ctx, containerID, argv)
	if err != nil { return fmt.Errorf("EasyTier 端口转发修改失败: %w", err) }
	return nil
}
