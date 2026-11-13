package reverse

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const promotionFileName = "reverse-promotion.json"

type promotionState struct {
	Endpoint string `json:"endpoint"`
}

func validatePromotionEndpoint(endpoint string) error {
	if len(endpoint) == 0 || len(endpoint) > 255 || strings.ContainsAny(endpoint, "\r\n\x00") {
		return errors.New("组网管理地址无效")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("组网管理地址必须是无路径的 HTTP 或 HTTPS 地址")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsGlobalUnicast() {
		return errors.New("组网管理地址必须使用有效的组网单播 IP")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("组网管理端口无效")
	}
	return nil
}

func SavePromotion(dataDir, endpoint string) error {
	if err := validatePromotionEndpoint(endpoint); err != nil {
		return err
	}
	data, err := json.Marshal(promotionState{Endpoint: endpoint})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".reverse-promotion-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dataDir, promotionFileName))
}

func LoadPromotion(dataDir string) (string, bool, error) {
	path := filepath.Join(dataDir, promotionFileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", false, fmt.Errorf("组网监听状态文件权限或类型不安全")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var state promotionState
	if err := json.Unmarshal(data, &state); err != nil || validatePromotionEndpoint(state.Endpoint) != nil {
		return "", false, errors.New("组网监听状态文件损坏")
	}
	return state.Endpoint, true, nil
}

func ClearPromotion(dataDir string) error {
	return removePrivateFile(filepath.Join(dataDir, promotionFileName))
}
