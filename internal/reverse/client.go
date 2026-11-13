// Package reverse maintains an outbound HTTP(S) control channel for hosts behind NAT.
// It intentionally uses short requests and does not expose a new inbound listener.
package reverse

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
	"github.com/CiBeDerORG/EasyHostAgent/internal/pairing"
)

const (
	credentialFileName = "reverse-credential.json"
	bootstrapFileName  = "reverse-bootstrap.json"
)

type credentials struct {
	ControllerID string `json:"controller_id"`
	HostID       string `json:"host_id,omitempty"`
	Token        string `json:"token"`
}

type discoveryReply struct {
	Status       string `json:"status"`
	ControllerID string `json:"controller_id"`
	HostID       string `json:"host_id"`
	AgentToken   string `json:"agent_token"`
	NextPoll     int    `json:"next_poll_seconds"`
}

type pollReply struct {
	Status       string       `json:"status"`
	ControllerID string       `json:"controller_id"`
	HostID       string       `json:"host_id"`
	ExpiresAt    string       `json:"expires_at"`
	NextPollAt   string       `json:"next_poll_at"`
	NextPoll     int          `json:"next_poll_seconds"`
	Task         *reverseTask `json:"task"`
}

type client struct {
	cfg        config.Config
	identity   identity.State
	version    string
	apps       *market.Service
	bundles    *bundles.Store
	logger     *slog.Logger
	http       *http.Client
	certDER    string
	credential string
	bootstrap  string
	pairing    *pairing.Store
}

func Start(ctx context.Context, cfg config.Config, state identity.State, version string, apps *market.Service, bundleStore *bundles.Store, pairingStore *pairing.Store, logger *slog.Logger) {
	if cfg.ControllerURL == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	if strings.HasPrefix(cfg.ControllerURL, "http://") {
		logger.Warn("reverse_agent_plain_http", "controller", cfg.ControllerURL,
			"warning", "HTTP 为明文连接，发现凭据、Agent Token 和安装参数可能被窃听；仅在可信隔离网络使用")
	}
	c := &client{
		cfg: cfg, identity: state, version: version, apps: apps, bundles: bundleStore, logger: logger,
		http: &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				MaxIdleConns:    2, IdleConnTimeout: 30 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		certDER:    base64.StdEncoding.EncodeToString(state.Certificate.Certificate[0]),
		credential: filepath.Join(cfg.DataDir, credentialFileName),
		bootstrap:  filepath.Join(cfg.DataDir, bootstrapFileName),
		pairing:    pairingStore,
	}
	go c.run(ctx)
}

func (c *client) endpoint(path string) string {
	return strings.TrimRight(c.cfg.ControllerURL, "/") + path
}

func (c *client) run(ctx context.Context) {
	c.logger.Info("reverse_agent_start", "controller", c.cfg.ControllerURL, "agent_id", c.identity.AgentID)
	failures := 0
	lastReport := time.Time{}
	for ctx.Err() == nil {
		cred, err := c.loadCredential()
		if err != nil {
			c.logger.Error("reverse_credential_unavailable", "error", err)
			return
		}
		if cred.Token == "" {
			bootstrap, loadErr := c.loadBootstrap()
			if loadErr != nil {
				c.logger.Error("reverse_bootstrap_unavailable", "error", loadErr)
				return
			}
			if bootstrap == "" {
				if !sleep(ctx, 10*time.Second) {
					return
				}
				continue
			}
			reply, status, err := c.discover(ctx, bootstrap)
			if err != nil {
				if status == http.StatusForbidden || status == http.StatusUnauthorized {
					c.logger.Warn("reverse_bootstrap_rejected", "status", status)
					_ = c.clearBootstrap()
				} else {
					failures++
					c.logger.Warn("reverse_discovery_failed", "attempt", failures, "error", err)
				}
				if !sleep(ctx, retryDelay(failures)) {
					return
				}
				continue
			}
			failures = 0
			if reply.Status == "accepted" && reply.AgentToken != "" && reply.ControllerID != "" && reply.HostID != "" {
				if c.pairing == nil {
					c.logger.Error("reverse_pairing_store_unavailable")
					return
				}
				if err := c.pairing.InstallGrant(reply.ControllerID, reply.HostID, reply.AgentToken); err != nil {
					c.logger.Error("reverse_grant_install_failed", "error", err)
					continue
				}
				if err := c.saveCredential(credentials{ControllerID: reply.ControllerID, HostID: reply.HostID, Token: reply.AgentToken}); err != nil {
					c.logger.Error("reverse_credential_save_failed", "error", err)
					return
				}
				if err := c.clearBootstrap(); err != nil {
					c.logger.Error("reverse_bootstrap_clear_failed", "error", err)
					return
				}
				lastReport = time.Time{}
				c.logger.Info("reverse_agent_accepted", "controller_id", reply.ControllerID, "host_id", reply.HostID)
				continue
			}
			if !sleep(ctx, seconds(reply.NextPoll, 10)) {
				return
			}
			continue
		}

		fullReport := lastReport.IsZero() || time.Since(lastReport) >= 10*time.Minute
		reply, status, err := c.poll(ctx, cred, fullReport)
		if err != nil {
			lastReport = time.Time{}
			if status == http.StatusGone {
				c.logger.Info("reverse_temporary_host_expired", "agent_id", c.identity.AgentID)
				if c.pairing != nil {
					_ = c.pairing.Revoke(cred.ControllerID, c.identity.AgentID, cred.Token)
				}
				_ = c.clearCredential()
				return
			}
			if status == http.StatusUnauthorized {
				c.logger.Warn("reverse_token_rejected", "agent_id", c.identity.AgentID)
				if c.pairing != nil {
					_ = c.pairing.Reset()
				}
				if clearErr := c.clearCredential(); clearErr != nil {
					c.logger.Error("reverse_credential_clear_failed", "error", clearErr)
					return
				}
				failures = 0
				lastReport = time.Time{}
				continue
			}
			failures++
			c.logger.Warn("reverse_poll_failed", "attempt", failures, "error", err)
			if !sleep(ctx, retryDelay(failures)) {
				return
			}
			continue
		}
		failures = 0
		if reply.ControllerID != "" && reply.HostID != "" && c.pairing != nil {
			if err := c.pairing.InstallGrant(reply.ControllerID, reply.HostID, cred.Token); err != nil {
				c.logger.Error("reverse_grant_refresh_failed", "error", err)
				if !sleep(ctx, 10*time.Second) {
					return
				}
				continue
			}
			if cred.HostID != reply.HostID || cred.ControllerID != reply.ControllerID {
				cred.HostID, cred.ControllerID = reply.HostID, reply.ControllerID
				if err := c.saveCredential(cred); err != nil {
					c.logger.Error("reverse_credential_refresh_failed", "error", err)
					return
				}
			}
		}
		if fullReport {
			lastReport = time.Now()
		}
		if reply.Status != "accepted" {
			c.logger.Warn("reverse_poll_rejected", "status", reply.Status)
			if !sleep(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if reply.Task != nil {
			if err := c.processTask(ctx, cred, *reply.Task); err != nil {
				c.logger.Warn("reverse_task_processing_failed", "task_id", reply.Task.ID, "error", err)
				if !sleep(ctx, 3*time.Second) {
					return
				}
			}
			continue
		}
		if !sleep(ctx, seconds(reply.NextPoll, 10)) {
			return
		}
	}
	c.logger.Info("reverse_agent_stop", "agent_id", c.identity.AgentID)
}

func (c *client) discover(ctx context.Context, token string) (discoveryReply, int, error) {
	payload := map[string]string{
		"agent_id": c.identity.AgentID, "hostname": hostname(),
		"spki_sha256": c.identity.SPKISHA256, "platform": c.cfg.Platform,
		"arch": runtimeArch(), "version": c.version, "certificate_der": c.certDER,
	}
	var reply discoveryReply
	status, err := c.post(ctx, "/api/v1/agent/discovery", token, payload, &reply)
	if err != nil {
		return reply, status, fmt.Errorf("发现请求 HTTP %d: %w", status, err)
	}
	return reply, status, nil
}

func (c *client) poll(ctx context.Context, cred credentials, fullReport bool) (pollReply, int, error) {
	var reply pollReply
	payload := map[string]any{"agent_id": c.identity.AgentID, "report": fullReport}
	if fullReport {
		payload["hostname"] = hostname()
		payload["spki_sha256"] = c.identity.SPKISHA256
		payload["platform"] = c.cfg.Platform
		payload["arch"] = runtimeArch()
		payload["version"] = c.version
		payload["certificate_der"] = c.certDER
	}
	status, err := c.post(ctx, "/api/v1/agent/poll", cred.Token, payload, &reply)
	if err != nil {
		return reply, status, fmt.Errorf("轮询请求 HTTP %d: %w", status, err)
	}
	return reply, status, nil
}

func (c *client) post(ctx context.Context, path, token string, payload, target any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiError struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &apiError)
		if apiError.Code == "" {
			apiError.Code = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, errors.New(apiError.Code + ": " + apiError.Message)
	}
	if target == nil {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return resp.StatusCode, errors.New("控制端响应格式无效")
	}
	return resp.StatusCode, nil
}


func validBootstrapToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func PairingStatus(dataDir string, store *pairing.Store) (bool, error) {
	if store == nil {
		return false, errors.New("配对存储不可用")
	}
	hasGrant, err := store.HasGrant()
	if err != nil {
		return false, err
	}
	for _, name := range []string{credentialFileName, bootstrapFileName} {
		path := filepath.Join(dataDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return false, fmt.Errorf("配对文件权限或类型不安全：%s", name)
		}
		return true, nil
	}
	return hasGrant, nil
}

// PreparePairing stores the one-time bootstrap token supplied interactively by deploy.sh.
func PreparePairing(dataDir, controllerURL, token string, replaceExisting bool, store *pairing.Store) error {
	if controllerURL == "" {
		return errors.New("请先在 deploy/.env 配置 EASYHOSTAGENT_CONTROLLER_URL")
	}
	if !validBootstrapToken(token) {
		return errors.New("Token 必须是 EasyNetTopo 生成的 64 位十六进制凭据")
	}
	paired, err := PairingStatus(dataDir, store)
	if err != nil {
		return err
	}
	if paired && !replaceExisting {
		return errors.New("当前主机已经配对；请先确认是否重新配对")
	}
	if err := saveBootstrap(dataDir, token); err != nil {
		return err
	}
	if paired {
		if err := store.Reset(); err != nil {
			_ = os.Remove(filepath.Join(dataDir, bootstrapFileName))
			return err
		}
		if err := removePrivateFile(filepath.Join(dataDir, credentialFileName)); err != nil {
			return err
		}
	}
	return nil
}

func saveBootstrap(dataDir, token string) error {
	data, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: token})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".reverse-bootstrap-*")
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
	return os.Rename(name, filepath.Join(dataDir, bootstrapFileName))
}

func (c *client) loadBootstrap() (string, error) {
	info, err := os.Lstat(c.bootstrap)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("一次性接入凭据文件权限或类型不安全")
	}
	data, err := os.ReadFile(c.bootstrap)
	if err != nil {
		return "", err
	}
	var value struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &value); err != nil || !validBootstrapToken(value.Token) {
		return "", errors.New("一次性接入凭据文件损坏")
	}
	return value.Token, nil
}

func (c *client) clearBootstrap() error {
	return removePrivateFile(c.bootstrap)
}

func removePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("拒绝删除非普通凭据文件")
	}
	return os.Remove(path)
}

func (c *client) loadCredential() (credentials, error) {
	info, err := os.Lstat(c.credential)
	if errors.Is(err, os.ErrNotExist) {
		return credentials{}, nil
	}
	if err != nil {
		return credentials{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return credentials{}, errors.New("反向接入凭据文件权限或类型不安全")
	}
	data, err := os.ReadFile(c.credential)
	if err != nil {
		return credentials{}, err
	}
	var result credentials
	if err := json.Unmarshal(data, &result); err != nil || len(result.Token) != 64 || result.ControllerID == "" {
		return credentials{}, errors.New("反向接入凭据文件损坏；拒绝自动重置")
	}
	return result, nil
}

func (c *client) saveCredential(value credentials) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.cfg.DataDir, ".reverse-credential-*")
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
	return os.Rename(name, c.credential)
}

func (c *client) clearCredential() error {
	info, err := os.Lstat(c.credential)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("拒绝删除非普通凭据文件")
	}
	return os.Remove(c.credential)
}

func hostname() string {
	value, err := os.Hostname()
	if err != nil || value == "" {
		return "unknown"
	}
	return value
}

func runtimeArch() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	default:
		return "other"
	}
}

func seconds(value int, fallback int) time.Duration {
	if value < 1 || value > 300 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func retryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 2 * time.Second
	case attempt == 2:
		return 4 * time.Second
	case attempt == 3:
		return 8 * time.Second
	case attempt == 4:
		return 15 * time.Second
	default:
		return 30 * time.Second
	}
}

func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
