// Package mcp 实现 MCP（Model Context Protocol）client，连接外部 MCP server。
//
// 协议：JSON-RPC 2.0 over HTTP（Streamable HTTP transport 的简化形态）。
// 平台作为 client 向 MCP server POST 单个 JSON-RPC 请求，同步等响应。
// 实现 initialize（协商）→ tools/list（列工具）→ tools/call（调用）。
//
// 设计权衡（KISS/YAGNI）：
//   - 不实现完整 SSE 流式 / session 管理 / 通知（MVP 用单次 request-response，多数 MCP server 支持）
//   - 纯 net/http 不引 SDK（与 qdrant/minio 适配器一致，减少依赖）
//   - 超时由调用方 ctx 控制（Agent 运行时派生 timeout ctx）
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/aitoys/paas/pkg/provider"
)

// Client 连接单个 MCP server（按 Tool.Config[serverURL]/[apiKey] 构造）。
type Client struct {
	serverURL string // MCP server HTTP 根地址（如 http://srv:8080，请求拼 /mcp）
	apiKey    string // Bearer 鉴权（可空）
	http      *http.Client
	idCounter atomic.Uint64
}

// NewClient 构造 MCP client。http.Client 不跟随重定向（CheckRedirect=ErrUseLastResponse，
// 防 serverURL 被配为攻击者主机返 302->Bearer apiKey 外发，复用 httputil.NewClient 模式）。
// Timeout=0 由 ctx 控制超时（MCP 调用可能长耗时）。
func NewClient(serverURL, apiKey string) *Client {
	return &Client{
		serverURL: serverURL,
		apiKey:    apiKey,
		http: &http.Client{
			Timeout: 0,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// jsonrpc 请求/响应结构（MCP 用 JSON-RPC 2.0）。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// call 发单个 JSON-RPC 请求，返 result（err 为 RPC error 或传输错误）。
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	req := rpcRequest{JSONRPC: "2.0", ID: c.idCounter.Add(1), Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: mcp 请求失败: %v", provider.ErrUpstreamUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: mcp server %d", provider.ErrUpstreamUnavailable, resp.StatusCode)
	}
	var rpc rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return nil, fmt.Errorf("解析 mcp 响应失败: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result, nil
}

// ToolDef MCP server 暴露的工具定义（tools/list 结果项）。
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"` // JSON Schema 参数定义
}

// CallResult tools/call 结果（MCP spec：content 数组 + isError 标记）。
type CallResult struct {
	Content []struct {
		Type string `json:"type"` // text | image | resource
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// InitializeParams / ListParams / CallParams（MCP spec 子集）。
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type listParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Initialize 与 MCP server 协商（必须先调，建立 session；MVP 仅协商不持久化 session）。
func (c *Client) Initialize(ctx context.Context) error {
	p := initializeParams{ProtocolVersion: "2025-06-18", Capabilities: map[string]any{}}
	p.ClientInfo.Name = "paas-core"
	p.ClientInfo.Version = "0.1.0"
	_, err := c.call(ctx, "initialize", p)
	return err
}

// ListTools 列 MCP server 暴露的工具（tools/list）。
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", listParams{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("解析 tools/list 失败: %w", err)
	}
	return res.Tools, nil
}

// Invoke 调用 MCP 工具（tools/call）。
func (c *Client) Invoke(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	raw, err := c.call(ctx, "tools/call", callParams{Name: name, Arguments: args})
	if err != nil {
		return CallResult{}, err
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("解析 tools/call 失败: %w", err)
	}
	if res.IsError {
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		return res, fmt.Errorf("mcp tool %s 返回错误: %s", name, text)
	}
	return res, nil
}

// factory 缓存（按 serverURL+apiKey 复用 client，避免每调用重建）。
var (
	clientCache   sync.Map // key -> *Client
)

// GetClient 按 serverURL+apiKey 取/建 client（缓存）。
func GetClient(serverURL, apiKey string) *Client {
	key := serverURL + "|" + apiKey
	if v, ok := clientCache.Load(key); ok {
		return v.(*Client)
	}
	c := NewClient(serverURL, apiKey)
	v, _ := clientCache.LoadOrStore(key, c)
	return v.(*Client)
}
