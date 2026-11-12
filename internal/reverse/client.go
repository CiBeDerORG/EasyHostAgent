// Package reverse implements the optional HTTPS pull transport. It does not
// replace or proxy the existing local EasyHostAgent HTTP/HTTPS API.
package reverse

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/identity"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
)

const pollInterval = 10 * time.Second

type Client struct {
	base        string
	bootstrap   string
	identity    identity.State
	platform    string
	version     string
	cache       *bundles.Store
	apps        *market.Service
	logger      *slog.Logger
	http        *http.Client
	recordDir   string
	mu          sync.Mutex
	activeID    string
	lastReport  time.Time
	lastSuccess time.Time
}

type reportReply struct {
	Status    string `json:"status"`
	HostID    string `json:"host_id"`
	ExpiresAt int64  `json:"expires_at"`
}
type task struct {
	TaskID       string            `json:"task_id"`
	ControllerID string            `json:"controller_id"`
	SourceID     string            `json:"source_id"`
	AppID        string            `json:"app_id"`
	Revision     string            `json:"revision"`
	BundleSHA256 string            `json:"bundle_sha256"`
	Config       map[string]string `json:"config"`
}
type record struct {
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	Delivered bool   `json:"delivered"`
}

func New(controllerURL, token, dir, platform, version string, id identity.State, cache *bundles.Store, apps *market.Service, logger *slog.Logger) (*Client, error) {
	if controllerURL == "" && token == "" {
		return nil, nil
	}
	if len(token) < 32 {
		return nil, errors.New("启用反向连接必须配置至少 32 字符的 EASYHOSTAGENT_REVERSE_BOOTSTRAP_TOKEN")
	}
	u, err := url.Parse(controllerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return nil, errors.New("EASYHOSTAGENT_CONTROLLER_URL 必须是无路径和凭据的 HTTPS 地址")
	}
	if _, ok := id.Certificate.PrivateKey.(*ecdsa.PrivateKey); !ok {
		return nil, errors.New("Agent 身份私钥不支持签名")
	}
	path := filepath.Join(dir, "reverse-tasks")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("反向任务状态目录权限不安全")
	}
	return &Client{
		base: strings.TrimRight(controllerURL, "/"), bootstrap: token, identity: id, platform: platform,
		version: version, cache: cache, apps: apps, logger: logger,
		http:      &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		recordDir: path,
	}, nil
}

type responseError struct {
	Code int
	Text string
}

func (e responseError) Error() string { return fmt.Sprintf("控制端响应 HTTP %d", e.Code) }
func (c *Client) call(ctx context.Context, method, path string, request any) (int, []byte, error) {
	var data []byte
	if request != nil {
		var err error
		data, err = json.Marshal(request)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return 0, nil, err
	}
	nonceText := hex.EncodeToString(nonce)
	timestamp := fmt.Sprint(time.Now().Unix())
	digest := sha256.Sum256(data)
	message := timestamp + "\n" + nonceText + "\n" + method + "\n" + req.URL.EscapedPath() + "\n" + hex.EncodeToString(digest[:])
	sum := sha256.Sum256([]byte(message))
	private := c.identity.Certificate.PrivateKey.(*ecdsa.PrivateKey)
	sig, err := ecdsa.SignASN1(rand.Reader, private, sum[:])
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.bootstrap)
	req.Header.Set("X-EHA-Agent-ID", c.identity.AgentID)
	req.Header.Set("X-EHA-Cert", base64.RawURLEncoding.EncodeToString(c.identity.Certificate.Certificate[0]))
	req.Header.Set("X-EHA-Timestamp", timestamp)
	req.Header.Set("X-EHA-Nonce", nonceText)
	req.Header.Set("X-EHA-Signature", base64.RawURLEncoding.EncodeToString(sig))
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	limit := int64(16 << 10)
	if strings.HasSuffix(path, "/bundle") {
		limit = bundles.MaxBundleSize + 1
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(body)) >= limit {
		return 0, nil, errors.New("控制端返回过大")
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, nil, responseError{Code: res.StatusCode}
	}
	return res.StatusCode, body, nil
}

func (c *Client) report(ctx context.Context) (reportReply, error) {
	var reply reportReply
	hostname, _ := os.Hostname()
	_, raw, err := c.call(ctx, "POST", "/api/v1/reverse/report", map[string]string{
		"hostname": hostname, "platform": c.platform, "arch": arch(), "version": c.version,
	})
	if err != nil {
		return reply, err
	}
	if err = json.Unmarshal(raw, &reply); err != nil {
		return reply, err
	}
	c.lastReport = time.Now()
	c.lastSuccess = time.Now()
	return reply, nil
}

func arch() string {
	// 由构建目标 GOARCH 得到体系结构，不使用 Docker 镜像中的宿主检测。
	return runtime.GOARCH
}

func (c *Client) claim(ctx context.Context) (*task, error) {
	status, raw, err := c.call(ctx, "POST", "/api/v1/reverse/claim", map[string]bool{})
	if err != nil {
		return nil, err
	}
	c.lastSuccess = time.Now()
	if status == http.StatusNoContent {
		return nil, nil
	}
	var t task
	if json.Unmarshal(raw, &t) != nil || t.TaskID == "" || t.ControllerID == "" ||
		t.SourceID == "" || t.AppID == "" || len(t.BundleSHA256) != 64 {
		return nil, errors.New("领取任务内容无效")
	}
	return &t, nil
}
func (c *Client) path(taskID string) (string, error) {
	if !strings.HasPrefix(taskID, "AT-") || len(taskID) > 80 || strings.ContainsAny(taskID, "/\\.\r\n") {
		return "", errors.New("非法任务标识")
	}
	return filepath.Join(c.recordDir, taskID+".json"), nil
}
func (c *Client) read(taskID string) (record, bool, error) {
	path, err := c.path(taskID)
	if err != nil {
		return record{}, false, err
	}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return record{}, false, errors.New("反向任务状态文件权限异常")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return record{}, false, err
	}
	var r record
	if json.Unmarshal(raw, &r) != nil || r.TaskID != taskID {
		return record{}, false, errors.New("反向任务状态损坏")
	}
	return r, true, nil
}
func (c *Client) save(r record) error {
	path, err := c.path(r.TaskID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.recordDir, ".reverse-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (c *Client) result(ctx context.Context, r record) error {
	_, _, err := c.call(ctx, "POST", "/api/v1/reverse/tasks/"+r.TaskID+"/result", map[string]string{"state": r.State, "message": r.Message})
	if err != nil {
		return err
	}
	r.Delivered = true
	return c.save(r)
}

// 任务执行前先原子保存本地状态。进程重启后无法确定的执行只报告中断，不会重复安装。
func (c *Client) execute(ctx context.Context, t task) {
	defer func() { c.mu.Lock(); c.activeID = ""; c.mu.Unlock() }()
	finish := func(state, message string) {
		r := record{TaskID: t.TaskID, State: state, Message: message}
		if err := c.save(r); err != nil {
			c.logger.Error("反向任务结果落盘失败", "task_id", t.TaskID, "error", err)
			return
		}
		rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := c.result(rctx, r); err != nil {
			c.logger.Warn("反向任务回传失败，将重试", "task_id", t.TaskID, "error", err)
		}
	}
	r, exists, err := c.read(t.TaskID)
	if err != nil {
		c.logger.Error("读取历史执行记录失败，拒绝重跑", "task_id", t.TaskID, "error", err)
		return
	}
	if exists {
		if r.State == "started" || r.State == "received" {
			finish("interrupted", "Agent 重启或任务执行状态不确定，禁止重复安装")
		} else if !r.Delivered {
			rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = c.result(rctx, r)
		}
		return
	}
	// 在下载前记录任务存在。收到相同 task_id 时不得再次执行。
	if err := c.save(record{TaskID: t.TaskID, State: "received"}); err != nil {
		c.logger.Error("反向任务不能持久化，拒绝领取", "error", err)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	_, blob, err := c.call(dctx, "GET", "/api/v1/reverse/tasks/"+t.TaskID+"/bundle", nil)
	cancel()
	if err != nil {
		finish("failed", "安装包下载失败")
		return
	}
	if _, _, err = c.cache.Put(ctx, t.BundleSHA256, bytes.NewReader(blob)); err != nil {
		finish("failed", "应用包校验失败")
		return
	}
	if _, _, err = c.call(ctx, "POST", "/api/v1/reverse/tasks/"+t.TaskID+"/ack", map[string]bool{}); err != nil {
		finish("interrupted", "任务领取确认失败，未执行安装")
		return
	}
	if err := c.save(record{TaskID: t.TaskID, State: "started"}); err != nil {
		finish("interrupted", "无法持久化安装开始状态")
		return
	}
	request := market.Request{Ref: market.Ref{Source: t.SourceID, App: t.AppID}, Digest: t.BundleSHA256, Config: t.Config}
	rec, err := c.apps.Start(ctx, t.ControllerID, "install", request, func() bool { return ctx.Err() == nil })
	if err != nil {
		finish("failed", "应用安装被 Agent 拒绝")
		return
	}
	if rec.Result == "failed" {
		finish("failed", "应用安装失败")
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(31 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			finish("interrupted", "Agent 已停止，安装结果未知")
			return
		case <-deadline.C:
			finish("interrupted", "安装执行超时，禁止自动重试")
			return
		case <-ticker.C:
			statuses, err := c.apps.Status(ctx, t.ControllerID, []market.Ref{{Source: t.SourceID, App: t.AppID}}, false)
			if err != nil || len(statuses) != 1 {
				continue
			}
			row := statuses[0]
			if row.Action != "install" || row.Digest != t.BundleSHA256 {
				continue
			}
			switch row.Result {
			case "succeeded":
				finish("succeeded", "")
				return
			case "failed":
				finish("failed", "安装脚本执行失败")
				return
			case "unknown":
				finish("interrupted", "本地安装结果未知")
				return
			}
		}
	}
}

func (c *Client) flush(ctx context.Context) {
	entries, err := os.ReadDir(c.recordDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "AT-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		taskID := strings.TrimSuffix(name, ".json")
		c.mu.Lock()
		active := c.activeID == taskID
		c.mu.Unlock()
		if active {
			continue
		}
		r, exists, err := c.read(taskID)
		if err != nil || !exists || r.Delivered {
			continue
		}
		if r.State == "started" || r.State == "received" {
			r.State = "interrupted"
			r.Message = "Agent 重新启动后无法确认上次执行结果"
			if c.save(r) != nil {
				continue
			}
		}
		_ = c.result(ctx, r)
	}
}

// Run 使用短轮询和有上限的重试；失败五分钟后优先完整报到。
// 固定临时过期不续期；410 表示停止领取新任务。
func (c *Client) Run(ctx context.Context) {
	backoff := 2 * time.Second
	restartReport := true
	var accepted bool
	var expired bool
	for ctx.Err() == nil {
		var err error
		if !expired && (restartReport || !accepted || time.Since(c.lastReport) >= 10*time.Minute ||
			(!c.lastSuccess.IsZero() && time.Since(c.lastSuccess) >= 5*time.Minute)) {
			var reply reportReply
			reply, err = c.report(ctx)
			if err == nil {
				accepted = reply.Status == "accepted"
				expired = reply.Status == "expired" || reply.Status == "revoked"
				restartReport = false
			}
		}
		if err == nil && accepted && !expired {
			c.flush(ctx)
			var job *task
			job, err = c.claim(ctx)
			if job != nil && err == nil {
				c.mu.Lock()
				if c.activeID == "" {
					c.activeID = job.TaskID
					go c.execute(ctx, *job)
				}
				c.mu.Unlock()
			}
		}
		if expired {
			// 已领取任务可在限定范围内回传结果，临时授权不会恢复。
			c.flush(ctx)
		}
		wait := pollInterval
		if err != nil {
			var status responseError
			if errors.As(err, &status) {
				if status.Code == http.StatusGone {
					expired = true
					accepted = false
				}
				if status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden {
					restartReport = true
				}
			}
			c.logger.Warn("反向接入请求失败", "error", err)
			wait = backoff
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		} else {
			backoff = 2 * time.Second
		}
		// 抖动减少一批 Agent 在同一时间报到造成的突发请求。
		var jitter [1]byte
		_, _ = rand.Read(jitter[:])
		wait += time.Duration(int(jitter[0])%1000) * time.Millisecond
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
