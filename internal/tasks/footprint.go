package tasks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// PortClaim 仅表示静态 Manifest 的默认值。运行时参数未支持，缺少默认值一律拒绝。
type PortClaim struct {
	HostIP    string `json:"host_ip"`
	Published uint16 `json:"published"`
	Protocol  string `json:"protocol"`
}
type Footprint struct {
	ComposeProject string      `json:"compose_project"`
	Ports          []PortClaim `json:"ports"`
	NamedVolumes   []string    `json:"named_volumes"`
	DataPaths      []string    `json:"data_paths"`
}
type configValue struct {
	Key     string `yaml:"key"`
	Type    string `yaml:"type"`
	Default any    `yaml:"default"`
}
type manifestClaim struct {
	SchemaVersion int    `yaml:"schema_version"`
	ID            string `yaml:"id"`
	Runtime       struct {
		Kind    string `yaml:"kind"`
		Project string `yaml:"compose_project"`
	} `yaml:"runtime"`
	Config []configValue `yaml:"config"`
	Ports  []struct {
		Protocol    string `yaml:"protocol"`
		HostPortKey string `yaml:"host_port_key"`
		BindKey     string `yaml:"bind_address_key"`
	} `yaml:"ports"`
	Data struct {
		Paths         []string `yaml:"paths"`
		NamedVolumes  []string `yaml:"named_volumes"`
		DefaultRetain bool     `yaml:"default_retain"`
	} `yaml:"data"`
}

var safeVolume = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// validateNode 在解析前拒绝 YAML 重复键、别名、合并映射及自定义类型，避免资源声明歧义。
func validateNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" || strings.HasPrefix(node.Tag, "!") {
		return ErrInvalid
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] || key.Value == "<<" {
				return ErrInvalid
			}
			seen[key.Value] = true
		}
	}
	for _, next := range node.Content {
		if err := validateNode(next); err != nil {
			return err
		}
	}
	return nil
}

func validDataPath(app, value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) < 3 || parts[0] != app {
		return false
	}
	hasData := false
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") ||
			strings.ContainsAny(part, "\\:\x00\r\n") {
			return false
		}
		if part == "data" {
			hasData = true
		}
	}
	return hasData
}

// ParseFootprint 从已经通过 Agent trust.Check 和包完整性校验的 Manifest 得到静态资源占用。
// 不执行 Compose、Shell 或变量展开。未知或动态配置始终失败关闭。
func ParseFootprint(raw []byte, approvedApp string) (Footprint, error) {
	if len(raw) == 0 || len(raw) > 128<<10 || !appID.MatchString(approvedApp) {
		return Footprint{}, ErrInvalid
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil || len(node.Content) != 1 {
		return Footprint{}, ErrInvalid
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Footprint{}, ErrInvalid
	}
	if err := validateNode(&node); err != nil {
		return Footprint{}, err
	}
	var m manifestClaim
	if err := node.Decode(&m); err != nil || m.SchemaVersion != 1 ||
		m.ID != approvedApp || m.Runtime.Kind != "docker_compose" ||
		m.Runtime.Project == "" || !safeVolume.MatchString(m.Runtime.Project) || !m.Data.DefaultRetain {
		return Footprint{}, ErrInvalid
	}
	config := map[string]configValue{}
	for _, value := range m.Config {
		if value.Key == "" || config[value.Key].Key != "" {
			return Footprint{}, ErrInvalid
		}
		config[value.Key] = value
	}
	result := Footprint{ComposeProject: m.Runtime.Project, Ports: []PortClaim{},
		NamedVolumes: []string{}, DataPaths: []string{}}
	usedPorts := map[string]bool{}
	for _, item := range m.Ports {
		kind := config[item.HostPortKey]
		if kind.Type != "port" {
			return Footprint{}, ErrInvalid
		}
		num, ok := kind.Default.(int)
		if !ok || num < 1 || num > 65535 {
			return Footprint{}, ErrInvalid
		}
		hostIP := "0.0.0.0"
		if item.BindKey != "" {
			bound := config[item.BindKey]
			value, ok := bound.Default.(string)
			if !ok || bound.Type != "ipv4" || net.ParseIP(value) == nil || net.ParseIP(value).To4() == nil {
				return Footprint{}, ErrInvalid
			}
			hostIP = value
		}
		protocol := item.Protocol
		if protocol != "tcp" && protocol != "udp" {
			return Footprint{}, ErrInvalid
		}
		claim := PortClaim{HostIP: hostIP, Published: uint16(num), Protocol: protocol}
		key := fmt.Sprintf("%s/%s/%d", claim.Protocol, claim.HostIP, claim.Published)
		if usedPorts[key] {
			return Footprint{}, ErrInvalid
		}
		usedPorts[key] = true
		result.Ports = append(result.Ports, claim)
	}
	for _, name := range m.Data.NamedVolumes {
		if !safeVolume.MatchString(name) {
			return Footprint{}, ErrInvalid
		}
		result.NamedVolumes = append(result.NamedVolumes, name)
	}
	for _, pathname := range m.Data.Paths {
		if !validDataPath(approvedApp, pathname) {
			return Footprint{}, ErrInvalid
		}
		result.DataPaths = append(result.DataPaths, pathname)
	}
	if len(result.Ports) > 128 || len(result.NamedVolumes) > 128 || len(result.DataPaths) > 128 {
		return Footprint{}, ErrInvalid
	}
	sort.Slice(result.Ports, func(i, j int) bool {
		a, b := result.Ports[i], result.Ports[j]
		return fmt.Sprint(a.Protocol, a.HostIP, a.Published) < fmt.Sprint(b.Protocol, b.HostIP, b.Published)
	})
	sort.Strings(result.NamedVolumes)
	sort.Strings(result.DataPaths)
	for _, values := range [][]string{result.NamedVolumes, result.DataPaths} {
		for i := 1; i < len(values); i++ {
			if values[i] == values[i-1] {
				return Footprint{}, ErrInvalid
			}
		}
	}
	return result, nil
}

func portOverlaps(a, b PortClaim) bool {
	if a.Protocol != b.Protocol || a.Published != b.Published {
		return false
	}
	return a.HostIP == b.HostIP || a.HostIP == "0.0.0.0" || b.HostIP == "0.0.0.0" ||
		a.HostIP == "" || b.HostIP == ""
}
func claimsConflict(a, b Footprint) bool {
	if a.ComposeProject == "" || b.ComposeProject == "" || a.ComposeProject == b.ComposeProject {
		return true
	}
	for _, left := range a.Ports {
		for _, right := range b.Ports {
			if portOverlaps(left, right) {
				return true
			}
		}
	}
	for _, left := range a.NamedVolumes {
		for _, right := range b.NamedVolumes {
			if left == right {
				return true
			}
		}
	}
	for _, left := range a.DataPaths {
		for _, right := range b.DataPaths {
			if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
				return true
			}
		}
	}
	return false
}

func parsePortNumber(value string) (uint16, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return 0, ErrInvalid
	}
	return uint16(n), nil
}
