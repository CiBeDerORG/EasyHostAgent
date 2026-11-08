package dockerengine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const maxExecOutput = 2 << 20

// readMultiplexed 仅解码 Docker API TTY=false 的 8 字节帧，不将 stderr 当作 JSON。
func readMultiplexed(data []byte) ([]byte, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	for len(data) != 0 {
		if len(data) < 8 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
			return nil, "", ErrProtocol
		}
		size := uint64(binary.BigEndian.Uint32(data[4:8]))
		if size > uint64(len(data)-8) {
			return nil, "", ErrProtocol
		}
		frame := data[8 : 8+int(size)]
		switch data[0] {
		case 1:
			if stdout.Len()+len(frame) > maxExecOutput {
				return nil, "", ErrProtocol
			}
			stdout.Write(frame)
		case 2:
			if stderr.Len()+len(frame) > maxExecOutput {
				return nil, "", ErrProtocol
			}
			stderr.Write(frame)
		default:
			return nil, "", ErrProtocol
		}
		data = data[8+int(size):]
	}
	return bytes.TrimSpace(stdout.Bytes()), stderr.String(), nil
}

// Exec 仅供已校验容器身份和命令白名单的 Driver 调用；不对外提供任意命令 API。
func (c *Client) Exec(ctx context.Context, containerID string, argv []string) ([]byte, error) {
	if !validContainerID(containerID) || len(argv) < 2 || len(argv) > 12 {
		return nil, ErrProtocol
	}
	for _, arg := range argv {
		if len(arg) == 0 || len(arg) > 256 {
			return nil, ErrProtocol
		}
	}
	snapshot, err := c.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	path := "http://docker.local/v" + snapshot.APIVersion
	post := func(urlPath string, body any, accepted int) ([]byte, error) {
		raw, e := json.Marshal(body)
		if e != nil {
			return nil, ErrProtocol
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, path+urlPath, bytes.NewReader(raw))
		if e != nil {
			return nil, ErrProtocol
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := c.http.Do(req)
		if e != nil {
			return nil, fmt.Errorf("%w: Docker Exec unreachable", ErrUnavailable)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, ErrPermission
		}
		if resp.StatusCode != accepted {
			return nil, fmt.Errorf("%w: Docker Exec HTTP %d", ErrUnavailable, resp.StatusCode)
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, maxExecOutput+1))
		if e != nil || len(data) > maxExecOutput {
			return nil, ErrProtocol
		}
		return data, nil
	}
	raw, err := post("/containers/"+url.PathEscape(containerID)+"/exec", map[string]any{
		"AttachStdout": true, "AttachStderr": true, "Tty": false, "Cmd": argv,
	}, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(raw, &created) != nil || !validContainerID(created.ID) {
		return nil, ErrProtocol
	}
	raw, err = post("/exec/"+url.PathEscape(created.ID)+"/start", map[string]bool{
		"Detach": false, "Tty": false,
	}, http.StatusOK)
	if err != nil {
		return nil, err
	}
	output, _, err := readMultiplexed(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path+"/exec/"+url.PathEscape(created.ID)+"/json", nil)
	if err != nil {
		return nil, ErrProtocol
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: Docker Exec inspect unavailable", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: Docker Exec inspect HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	var info struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&info); err != nil {
		return nil, errors.Join(ErrProtocol, err)
	}
	if info.Running || info.ExitCode != 0 {
		return nil, fmt.Errorf("EasyTier CLI 退出码 %d", info.ExitCode)
	}
	return output, nil
}
