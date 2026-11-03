// Package market 执行已在本机授权的应用源提供的 Docker 应用动作。
package market

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"gopkg.in/yaml.v3"
)

var appID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
var uuid = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var fieldKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,119}$`)

type action struct {
	Strategy      string
	Path          string
	Argv          []string
	RemoveOrphans bool `yaml:"remove_orphans"`
}

type field struct {
	Key, Type, Target string
	Required          bool
	Editable          *bool
	Default           *string
	Minimum, Maximum  *int
	MinLength         *int `yaml:"min_length"`
	MaxLength         *int `yaml:"max_length"`
	Pattern           string
}

type manifest struct {
	ID      string
	Runtime struct {
		Kind         string
		Project      string `yaml:"compose_project"`
		File         string `yaml:"compose_file"`
		RequiresRoot bool   `yaml:"requires_root"`
	}
	Compatibility struct{ Platforms, Architectures []string }
	Actions       map[string]action
	Config        []field
	Verification  struct {
		Services []string `yaml:"required_services"`
	}
	Data struct {
		Retain bool `yaml:"default_retain"`
	}
}

func loadManifest(info bundles.CandidateInfo, files map[string][]byte) (manifest, error) {
	var m manifest
	id := info.ApplicationID
	err := yaml.Unmarshal(files[id+"/app.yaml"], &m)
	if err != nil || m.ID != id || m.Runtime.Kind != "docker_compose" || m.Runtime.Project != id ||
		files[id+"/"+m.Runtime.File] == nil || !m.Data.Retain || len(m.Verification.Services) == 0 {
		return m, errors.New("应用的 Compose 清单无效")
	}
	for _, name := range []string{"install", "update", "uninstall"} {
		a, exists := m.Actions[name]
		if name == "update" && !exists {
			continue // Old application manifests may lack update; Start rejects when absent.
		}
		if a.Strategy == "compose_down" && name == "uninstall" && a.Path == "" && len(a.Argv) == 0 && !a.RemoveOrphans {
			continue
		}
		if a.Strategy != "script" || a.RemoveOrphans || !strings.HasSuffix(a.Path, ".sh") ||
			len(a.Argv) > 1 || (len(a.Argv) == 1 && a.Argv[0] != name) {
			return m, errors.New("应用缺少受支持的安装或卸载动作")
		}
		if name == "uninstall" && a.Path == m.Actions["install"].Path && len(a.Argv) == 0 {
			return m, errors.New("共用脚本缺少 uninstall 参数")
		}
		found := false
		for _, f := range info.Files {
			if f.Destination == id+"/"+a.Path && f.Mode == "0755" {
				found = true
				break
			}
		}
		if !found {
			return m, errors.New("动作脚本未列入可执行包文件")
		}
	}
	return m, nil
}

func (f field) validate(v string) error {
	if !fieldKey.MatchString(f.Key) || (f.Target != "env" && f.Target != "secrets") || (f.Type == "secret") != (f.Target == "secrets") {
		return errors.New("清单配置字段无效")
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("%s 不能写入单行配置", f.Key)
	}
	if v == "" && !f.Required {
		return nil
	}
	valid := v != ""
	switch f.Type {
	case "port", "number":
		n, err := strconv.Atoi(v)
		valid = err == nil && (f.Type != "port" || n >= 1 && n <= 65535) &&
			(f.Minimum == nil || n >= *f.Minimum) && (f.Maximum == nil || n <= *f.Maximum)
	case "boolean":
		valid = v == "true" || v == "false"
	case "ipv4":
		valid = net.ParseIP(v).To4() != nil
	case "string", "secret":
		n := utf8.RuneCountInString(v)
		valid = valid && (f.MinLength == nil || n >= *f.MinLength) && (f.MaxLength == nil || n <= *f.MaxLength)
	default:
		valid = false
	}
	if f.Pattern != "" {
		match, err := regexp.MatchString(f.Pattern, v)
		valid = valid && err == nil && match
	}
	if !valid {
		return fmt.Errorf("请检查配置字段 %s", f.Key)
	}
	return nil
}

// 私有应用根内保留稳定的数据目录；包文件使用原子替换，不跟随目标符号链接。
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("应用目录权限或类型不安全")
	}
	return nil
}

// 逐级核对软件根目录，避免从共享可写目录替换将以 Agent 身份执行的脚本。
func ensureSoftwareRoot(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("软件根目录必须为绝对路径")
	}
	current := "/"
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		st, err := os.Lstat(current)
		if err != nil || !st.IsDir() {
			return errors.New("软件根目录不可经过符号链接")
		}
		uid := st.Sys().(*syscall.Stat_t).Uid
		if (uid != 0 && uid != uint32(os.Geteuid())) || (st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0) {
			return errors.New("软件根目录不能由其他用户替换")
		}
	}
	return nil
}

func writeFile(name string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

func materialize(root string, info bundles.CandidateInfo, files map[string][]byte) error {
	for _, f := range info.Files {
		dir := root
		for _, part := range strings.Split(filepath.Dir(f.Destination), "/") {
			dir = filepath.Join(dir, part)
			if err := privateDir(dir); err != nil {
				return err
			}
		}
		mode := os.FileMode(0644)
		if f.Mode == "0755" {
			mode = 0755
		}
		target := filepath.Join(root, f.Destination)
		if strings.HasPrefix(f.Destination, info.ApplicationID+"/config/") {
			// 已存在的用户配置不随安装包更新覆盖，也不能通过符号链接绕过隔离目录。
			st, err := os.Lstat(target)
			if err == nil {
				if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
					return errors.New("已有用户配置文件不安全，拒绝覆盖")
				}
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := writeFile(target, files[f.Destination], mode); err != nil {
			return err
		}
	}
	return nil
}

// 仓库脚本按字面量读取 KEY=VALUE，只移除首尾一层引号，不执行 shell 展开。
type configFile struct {
	lines  []string
	values map[string]string
	index  map[string]int
}

func readConfig(path string) (*configFile, error) {
	c := &configFile{values: map[string]string{}, index: map[string]int{}}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 1<<20 {
		return nil, errors.New("配置文件类型或权限无效")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		i := len(c.lines)
		c.lines = append(c.lines, line)
		s := strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t")
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		if !ok || !fieldKey.MatchString(k) {
			return nil, errors.New("配置文件含有无效字段")
		}
		if _, exists := c.index[k]; exists {
			return nil, errors.New("配置文件有重复字段")
		}
		if strings.HasPrefix(v, "'") || strings.HasPrefix(v, "\"") {
			if len(v) < 2 || v[len(v)-1] != v[0] {
				return nil, errors.New("配置文件引号未闭合")
			}
			v = v[1 : len(v)-1]
		}
		c.index[k] = i
		c.values[k] = v
	}
	return c, nil
}

func prepareConfig(root string, fields []field, input map[string]string, update bool) (map[string]string, error) {
	dir := filepath.Join(root, "deploy")
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	configs := map[string]*configFile{}
	for _, target := range []string{"env", "secrets"} {
		c, err := readConfig(filepath.Join(dir, "."+target))
		if err != nil {
			return nil, err
		}
		configs[target] = c
	}
	values := map[string]string{}
	for _, f := range fields {
		c := configs[f.Target]
		if c == nil {
			return nil, errors.New("未知配置目标")
		}
		if _, dup := values[f.Key]; dup {
			return nil, errors.New("重复配置字段")
		}
		v, stored := c.values[f.Key]
		in, provided := input[f.Key]
		if f.Type == "secret" && stored && v != "" && provided && in != "" && in != v {
			return nil, fmt.Errorf("%s 已有凭据，重新安装将保留原值", f.Key)
		}
		if provided && f.Editable != nil && !*f.Editable {
			return nil, errors.New("配置字段只读")
		}
		if provided && !(f.Type == "secret" && in == "" && stored) {
			v = in
		} else if !stored && f.Default != nil {
			v = *f.Default
		}
		if update {
			if err := f.validate(v); err != nil {
				return nil, err
			}
			line := f.Key + "=\"" + v + "\""
			if i, ok := c.index[f.Key]; ok {
				c.lines[i] = line
			} else {
				c.lines = append(c.lines, line)
			}
		}
		values[f.Key] = v
	}
	for k := range input {
		if _, ok := values[k]; !ok {
			return nil, errors.New("请求含有未声明的配置字段")
		}
	}
	if update {
		for _, target := range []string{"env", "secrets"} {
			if err := writeFile(filepath.Join(dir, "."+target), []byte(strings.Join(configs[target].lines, "\n")+"\n"), 0600); err != nil {
				return nil, err
			}
		}
	}
	return values, nil
}
