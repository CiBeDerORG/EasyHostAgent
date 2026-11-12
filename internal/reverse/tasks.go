package reverse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/market"
)

type reverseTask struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`
	SourceID  string            `json:"source_id"`
	AppID     string            `json:"app_id"`
	Action    string            `json:"action"`
	Revision  string            `json:"revision"`
	BundleSHA string            `json:"bundle_sha256"`
	Config    map[string]string `json:"config"`
	BundleURL string            `json:"bundle_url"`
}

type taskLedger struct {
	TaskID    string `json:"task_id"`
	SourceID  string `json:"source_id"`
	AppID     string `json:"app_id"`
	BundleSHA string `json:"bundle_sha256"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

func validReverseTaskID(value string) bool {
	if len(value) != 39 || !strings.HasPrefix(value, "AT-") {
		return false
	}
	for _, ch := range value[3:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || ch == '-') {
			return false
		}
	}
	return true
}

func (c *client) ledgerPath(taskID string) (string, error) {
	if !validReverseTaskID(taskID) {
		return "", errors.New("反向任务 ID 无效")
	}
	return filepath.Join(c.cfg.DataDir, "reverse-task-"+taskID+".json"), nil
}

func (c *client) readLedger(task reverseTask) (taskLedger, bool, error) {
	path, err := c.ledgerPath(task.ID)
	if err != nil {
		return taskLedger{}, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return taskLedger{}, false, nil
	}
	if err != nil {
		return taskLedger{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return taskLedger{}, false, errors.New("本地反向任务记录权限或文件类型不安全")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return taskLedger{}, false, err
	}
	var result taskLedger
	if err := json.Unmarshal(data, &result); err != nil || result.TaskID != task.ID ||
		result.SourceID != task.SourceID || result.AppID != task.AppID || result.BundleSHA != task.BundleSHA {
		return taskLedger{}, false, errors.New("本地反向任务记录损坏或与任务不匹配")
	}
	return result, true, nil
}

func (c *client) writeLedger(value taskLedger) error {
	path, err := c.ledgerPath(value.TaskID)
	if err != nil {
		return err
	}
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.cfg.DataDir, ".reverse-task-*")
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
	return os.Rename(name, path)
}

func (c *client) processTask(ctx context.Context, cred credentials, task reverseTask) error {
	if task.Action != "install" || task.SourceID == "" || task.AppID == "" ||
		!bundles.ValidDigest(task.BundleSHA) || task.Revision == "" ||
		task.BundleURL != "/api/v1/agent/tasks/"+task.ID+"/bundle" || c.apps == nil || c.bundles == nil {
		return errors.New("控制端下发了不受支持或不完整的预安装任务")
	}
	ledger, exists, err := c.readLedger(task)
	if err != nil {
		return err
	}
	if exists && (ledger.State == "succeeded" || ledger.State == "failed" || ledger.State == "interrupted") {
		return c.reportTask(ctx, cred, task.ID, ledger.State, ledger.Message)
	}

	if task.State == "running" {
		// 已 ACK 的任务在 Agent 重启后只能核对结果，绝不重新运行有副作用的安装脚本。
		if !exists || ledger.State == "claimed" {
			return c.reportTask(ctx, cred, task.ID, "interrupted", "Agent 重启后无法确认安装是否开始；为避免重复安装，任务已中断")
		}
		return c.resumeTask(ctx, cred, task, ledger)
	}

	if !exists {
		ledger = taskLedger{TaskID: task.ID, SourceID: task.SourceID, AppID: task.AppID, BundleSHA: task.BundleSHA, State: "claimed"}
		if err := c.writeLedger(ledger); err != nil {
			return err
		}
	}
	if ledger.State != "claimed" {
		return c.reportTask(ctx, cred, task.ID, "interrupted", "本地任务状态与服务端队列不一致；为避免重复执行，任务已中断")
	}
	if err := c.postTaskAction(ctx, cred, task.ID, "ack", map[string]string{"agent_id": c.identity.AgentID}); err != nil {
		return err
	}
	ledger.State = "acked"
	if err := c.writeLedger(ledger); err != nil {
		return err
	}

	bundle, err := c.downloadBundle(ctx, cred, task)
	if err != nil {
		ledger.State, ledger.Message = "failed", "下载应用包失败："+err.Error()
		_ = c.writeLedger(ledger)
		_ = c.reportTask(ctx, cred, task.ID, "failed", ledger.Message)
		return err
	}
	if _, _, err := c.bundles.Put(ctx, task.BundleSHA, bytes.NewReader(bundle)); err != nil {
		ledger.State, ledger.Message = "failed", "应用包 SHA-256 或 ZIP 安全校验失败"
		_ = c.writeLedger(ledger)
		_ = c.reportTask(ctx, cred, task.ID, "failed", ledger.Message)
		return err
	}

	for c.apps.Busy() {
		if !sleep(ctx, time.Second) {
			return ctx.Err()
		}
	}
	ref := market.Ref{Source: task.SourceID, App: task.AppID}
	request := market.Request{Ref: ref, Digest: task.BundleSHA, Config: task.Config}
	var record market.Record
	for {
		record, err = c.apps.Start(ctx, cred.ControllerID, "install", request, func() bool { return true })
		if !errors.Is(err, market.ErrBusy) {
			break
		}
		if !sleep(ctx, time.Second) {
			return ctx.Err()
		}
	}
	if err != nil {
		ledger.State, ledger.Message = "failed", "Agent 拒绝预安装任务："+err.Error()
		_ = c.writeLedger(ledger)
		_ = c.reportTask(ctx, cred, task.ID, "failed", ledger.Message)
		return err
	}
	ledger.State = "running"
	if record.Result == "succeeded" {
		ledger.State = "succeeded"
	}
	if err := c.writeLedger(ledger); err != nil {
		return err
	}
	if ledger.State == "succeeded" {
		return c.reportTask(ctx, cred, task.ID, "succeeded", "")
	}
	return c.monitorTask(ctx, cred, task, ledger)
}

func (c *client) resumeTask(ctx context.Context, cred credentials, task reverseTask, ledger taskLedger) error {
	ref := market.Ref{Source: task.SourceID, App: task.AppID}
	rows, err := c.apps.Status(ctx, cred.ControllerID, []market.Ref{ref}, false)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Ref == ref && row.Digest == task.BundleSHA {
			switch row.Result {
			case "succeeded":
				ledger.State, ledger.Message = "succeeded", ""
				_ = c.writeLedger(ledger)
				return c.reportTask(ctx, cred, task.ID, "succeeded", "")
			case "failed":
				ledger.State, ledger.Message = "failed", row.Message
				_ = c.writeLedger(ledger)
				return c.reportTask(ctx, cred, task.ID, "failed", row.Message)
			case "running":
				ledger.State = "running"
				_ = c.writeLedger(ledger)
				return c.monitorTask(ctx, cred, task, ledger)
			}
		}
	}
	ledger.State, ledger.Message = "interrupted", "Agent 重启后本机安装记录无法证明任务仍在执行；为避免重复安装，任务已中断"
	_ = c.writeLedger(ledger)
	return c.reportTask(ctx, cred, task.ID, "interrupted", ledger.Message)
}

func (c *client) monitorTask(ctx context.Context, cred credentials, task reverseTask, ledger taskLedger) error {
	deadline := time.NewTimer(30 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		rows, err := c.apps.Status(ctx, cred.ControllerID, []market.Ref{{Source: task.SourceID, App: task.AppID}}, false)
		if err == nil {
			for _, row := range rows {
				if row.Ref != (market.Ref{Source: task.SourceID, App: task.AppID}) || row.Digest != task.BundleSHA {
					continue
				}
				switch row.Result {
				case "succeeded":
					ledger.State, ledger.Message = "succeeded", ""
					if err := c.writeLedger(ledger); err != nil {
						return err
					}
					return c.reportTask(ctx, cred, task.ID, "succeeded", "")
				case "failed":
					ledger.State, ledger.Message = "failed", row.Message
					if err := c.writeLedger(ledger); err != nil {
						return err
					}
					return c.reportTask(ctx, cred, task.ID, "failed", row.Message)
				case "unknown":
					ledger.State, ledger.Message = "interrupted", "本机安装结果未知；需要人工核对，禁止自动重跑"
					_ = c.writeLedger(ledger)
					return c.reportTask(ctx, cred, task.ID, "interrupted", ledger.Message)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			ledger.State, ledger.Message = "interrupted", "预安装执行超过 30 分钟，结果未知，需要人工核对"
			_ = c.writeLedger(ledger)
			_ = c.reportTask(ctx, cred, task.ID, "interrupted", ledger.Message)
			return errors.New(ledger.Message)
		case <-ticker.C:
		}
	}
}

func (c *client) downloadBundle(ctx context.Context, cred credentials, task reverseTask) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(task.BundleURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("X-EHA-Agent-ID", c.identity.AgentID)
	req.Header.Set("Accept", "application/zip")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bundle 下载 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, bundles.MaxBundleSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) <= 0 || int64(len(data)) > bundles.MaxBundleSize {
		return nil, bundles.ErrTooLarge
	}
	if resp.Header.Get("X-Bundle-SHA256") != task.BundleSHA {
		return nil, bundles.ErrDigestMismatch
	}
	return data, nil
}

func (c *client) postTaskAction(ctx context.Context, cred credentials, taskID, action string, payload any) error {
	_, err := c.post(ctx, "/api/v1/agent/tasks/"+taskID+"/"+action, cred.Token, payload, nil)
	return err
}

func (c *client) reportTask(ctx context.Context, cred credentials, taskID, state, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	return c.postTaskAction(ctx, cred, taskID, "status", map[string]string{
		"agent_id": c.identity.AgentID, "state": state, "message": message,
	})
}
