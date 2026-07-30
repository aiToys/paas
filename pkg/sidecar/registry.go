// Package sidecar 是数据面服务接入平台治理的轻量客户端 SDK。
// 第三方服务进程启动时调 Register 注册实例到 governance，运行期 Heartbeat 保活，退出时 Deregister。
// 纯 net/http 零依赖（除标准库），便于业务进程引入。
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client 是治理控制面客户端。
type Client struct {
	BaseURL string // 如 "http://paas-core:8080"
	APIKey  string // Bearer 凭证（绑定 租户/角色）
	HTTP    *http.Client
}

// NewClient 创建客户端，默认 10s 超时。
func NewClient(baseURL, apiKey string) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Instance 注册体（对齐 governance.Instance）。
type Instance struct {
	ServiceID string            `json:"serviceId"`
	Addr      string            `json:"addr"`
	LaneID    string            `json:"laneId"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Register 注册实例，返回新建实例 ID（由控制面分配，在 Location 或 body）。
func (c *Client) Register(ctx context.Context, in Instance) (string, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/services/"+in.ServiceID+"/instances", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("注册失败: HTTP %d", resp.StatusCode)
	}
	var wrap struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&wrap)
	return wrap.Data.ID, nil
}

// Heartbeat 发送心跳保活。
func (c *Client) Heartbeat(ctx context.Context, instanceID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.BaseURL+"/api/instances/"+instanceID+"/heartbeat", nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("心跳失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Deregister 注销实例。
func (c *Client) Deregister(ctx context.Context, serviceID, instanceID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.BaseURL+"/api/services/"+serviceID+"/instances/"+instanceID, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("注销失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) auth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

// KeepAlive 阻塞定期心跳，直到 ctx 取消；返回前最终心跳错误通过 errc 传出（可选）。
// 典型用法：go client.KeepAlive(ctx, instanceID, 30*time.Second)
func (c *Client) KeepAlive(ctx context.Context, instanceID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Heartbeat(ctx, instanceID)
		}
	}
}
