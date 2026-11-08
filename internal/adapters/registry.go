// Package adapters 在 Agent 进程内分发无状态 Driver，不创建适配器实例或任务数据库。
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/CiBeDerORG/EasyHostAgent/internal/adapters/easytier"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

var ErrUnknown = errors.New("不支持的 Driver 类型")
var ErrDisabled = errors.New("Driver 尚未启用")

type Driver interface {
	Type() string
	Capabilities() []string
	Read(context.Context, string, string) (json.RawMessage, error)
}

type Type struct {
	Type         string   `json:"type"`
	Enabled      bool     `json:"enabled"`
	Capabilities []string `json:"capabilities"`
}

type settings struct {
	Enabled map[string]bool `json:"enabled"`
}

// Service 的注册表只在内存里；唯一的持久文件仅用于恢复管理员设置的 Driver 开关。
type Service struct {
	mu      sync.RWMutex
	path    string
	drivers map[string]Driver
	enabled map[string]bool
}

func New(dataDir string, engine *dockerengine.Client) (*Service, error) {
	if dataDir == "" || engine == nil {
		return nil, errors.New("Driver 初始化参数无效")
	}
	driver := easytier.New(engine)
	s := &Service{
		path:    filepath.Join(dataDir, "adapter-settings.json"),
		drivers: map[string]Driver{driver.Type(): driver},
		enabled: map[string]bool{},
	}
	st, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil // 新 Agent 默认关闭所有 Driver。
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return nil, errors.New("Driver 设置文件类型或权限不安全")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var saved settings
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, errors.New("Driver 设置文件损坏")
	}
	for name, enabled := range saved.Enabled {
		if _, ok := s.drivers[name]; !ok {
			return nil, fmt.Errorf("设置包含未知 Driver: %s", name)
		}
		s.enabled[name] = enabled
	}
	return s, nil
}

func (s *Service) Types() []Type {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.drivers))
	for name := range s.drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Type, 0, len(names))
	for _, name := range names {
		driver := s.drivers[name]
		out = append(out, Type{Type: name, Enabled: s.enabled[name], Capabilities: driver.Capabilities()})
	}
	return out
}

func (s *Service) Set(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.drivers[name]; !ok {
		return ErrUnknown
	}
	// 只允许持久化已注册 Driver 的启用状态。
	next := make(map[string]bool, len(s.enabled)+1)
	for key, value := range s.enabled {
		next[key] = value
	}
	next[name] = enabled
	raw, err := json.Marshal(settings{Enabled: next})
	if err != nil {
		return err
	}
	// 不跟随攻击者创建的同名符号链接，临时文件与配置在同目录原子替换。
	if st, err := os.Lstat(s.path); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return errors.New("Driver 设置文件类型或权限不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".adapter-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.enabled = next
	return nil
}

func (s *Service) Read(ctx context.Context, name, resourceID, capability string) (json.RawMessage, error) {
	s.mu.RLock()
	driver, ok := s.drivers[name]
	enabled := s.enabled[name]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrUnknown
	}
	if !enabled {
		return nil, ErrDisabled
	}
	return driver.Read(ctx, resourceID, capability)
}
