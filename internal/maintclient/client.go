// Package maintclient 是面板进程访问本地维护 Socket 的客户端。
package maintclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/dreamstation625/ovpanel/internal/maintapi"
)

// Client 访问面板容器内的维护 Unix Socket。
type Client struct {
	http *http.Client
}

// New 创建维护客户端；socket 不存在时调用返回明确错误。
func New(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 10 * time.Second,
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

// State 读取维护进程状态。
func (c *Client) State(ctx context.Context) (maintapi.State, error) {
	var state maintapi.State
	if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &state); err != nil {
		return state, err
	}
	return state, nil
}

// StartUpdate 请求更新到指定标签。
func (c *Client) StartUpdate(ctx context.Context, tag, backupID string) (maintapi.Task, error) {
	body := maintapi.UpdateRequest{TargetTag: tag, BackupID: backupID}
	var response struct {
		Task maintapi.Task `json:"task"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/update", body, &response); err != nil {
		return maintapi.Task{}, err
	}
	return response.Task, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://maintenance"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("维护进程不可用: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload maintapi.ErrorResponse
		if err := json.Unmarshal(data, &payload); err == nil && payload.Error != "" {
			return fmt.Errorf("%s", payload.Error)
		}
		return fmt.Errorf("维护进程返回 %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析维护状态: %w", err)
		}
	}
	return nil
}
