package maas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/provider"
)

// openaiReq 是 OpenAI 兼容协议的请求体（仅取推理所需字段）。
// DeepSeek / 通义千问 DashScope 兼容模式 / OpenAI 三家同构。
type openaiReq struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	MaxTokens   *int               `json:"max_tokens,omitempty"`
}

// openaiDelta 是流式增量响应（仅取 choices[0].delta）。
// ReasoningContent 是推理模型（DeepSeek-R1/GLM/QwQ/Doubao 等）的思考过程增量，
// 与 Content 正交：思考阶段 content=null + reasoning_content=...，答案阶段反之。
// 不解析则思考过程被丢弃，前端长时间空白「不像流式」。
type openaiDelta struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
}

// OpenAICompatibleProvider 对接所有 OpenAI 兼容协议的供应商
// （OpenAI / DeepSeek / 通义千问 DashScope 兼容模式）。纯 net/http，无第三方 SDK。
//
// 三家端点拼 /chat/completions 一致：
//   - OpenAI:    https://api.openai.com/v1                      + /chat/completions
//   - DeepSeek:  https://api.deepseek.com                       + /chat/completions
//   - 通义千问:  https://dashscope.aliyuncs.com/compatible-mode/v1 + /chat/completions
//
// 凭证明文仅内存：经 CredentialResolver 运行时解析，不写日志、不进响应、不持久化。
type OpenAICompatibleProvider struct {
	vendor        string // 展示用（openai/deepseek/qwen），便于观测区分
	baseURL       string // 不含 /chat/completions 的根
	upstreamModel string // 供应商侧模型名（如 deepseek-chat / qwen-plus / gpt-4o）
	credentialRef string // security 平台级 Secret ID
	resolver      provider.CredentialResolver
	httpClient    *http.Client
}

// NewOpenAICompatibleProvider 构造一个第三方供应商通道。httpClient 为 nil 时用默认（120s 超时）。
func NewOpenAICompatibleProvider(vendor, baseURL, upstreamModel, credentialRef string, resolver provider.CredentialResolver, httpClient *http.Client) *OpenAICompatibleProvider {
	if httpClient == nil {
		// 不跟随重定向（httputil.NewClient 内置 CheckRedirect=ErrUseLastResponse）：
		// 防 baseURL 被配为攻击者主机返 302→metadata，或供应商端点被劫持重定向时，
		// 平台 airouter Key（Authorization 头）被外发。
		httpClient = httputil.NewClient(120 * time.Second)
	}
	return &OpenAICompatibleProvider{
		vendor:        vendor,
		baseURL:       baseURL,
		upstreamModel: upstreamModel,
		credentialRef: credentialRef,
		resolver:      resolver,
		httpClient:    httpClient,
	}
}

// Vendor 返回供应商展示名（观测/日志用，不输出明文 Key）。
func (p *OpenAICompatibleProvider) Vendor() string { return p.vendor }

func (p *OpenAICompatibleProvider) Name() string { return "openai-compatible" }

// Chat 发起流式推理：解析凭证 → POST 上游 → 逐行解析 SSE → Chunk 流。
// 失败按错误类型返回 sentinel（见 pkg/provider.Err*），由 Gateway 决定降级/failover。
func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
	if p.resolver == nil {
		return nil, provider.ErrCredentialMissing
	}
	apiKey, err := p.resolver.Resolve(p.credentialRef)
	if err != nil || apiKey == "" {
		return nil, provider.ErrCredentialMissing
	}

	body, _ := json.Marshal(openaiReq{
		Model:       p.upstreamModel, // 转换为供应商侧模型名
		Messages:    req.Messages,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})
	endpoint := strings.TrimRight(p.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	// 显式禁 gzip：Go Transport 默认自动加 Accept-Encoding: gzip 并透明解压，部分网关在
	// HTTP/2 下返回 gzip body 解压异常，会导致 SSE 行被跳过。identity 要求上游返明文 SSE 流。
	httpReq.Header.Set("Accept-Encoding", "identity")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, classifyErr(err, 0)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, classifyErr(nil, resp.StatusCode)
	}

	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 容纳长 SSE 行
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				return
			}
			var d openaiDelta
			if json.Unmarshal([]byte(payload), &d) != nil {
				continue // 跳过无法解析的行（如上游心跳/注释）
			}
			if len(d.Choices) == 0 {
				continue
			}
			delta := d.Choices[0].Delta
			if delta.Role == "" && delta.Content == "" && delta.ReasoningContent == "" {
				continue
			}
			select {
			case <-ctx.Done():
				return // 客户端断连，立即停止解析（不计费、不泄漏 goroutine）
			case ch <- provider.Chunk{Role: delta.Role, Content: delta.Content, Reasoning: delta.ReasoningContent}:
			}
		}
	}()
	return ch, nil
}

// classifyErr 把上游错误映射为降级 sentinel（驱动 Gateway failover 决策）。
func classifyErr(netErr error, status int) error {
	if netErr != nil {
		// 网络层错误（含超时、连接拒绝）→ 不可用，可 failover
		return fmt.Errorf("%w: %v", provider.ErrUpstreamUnavailable, netErr)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.ErrCredentialInvalid
	case status == http.StatusTooManyRequests:
		return provider.ErrUpstreamRateLimit
	case status >= 500:
		return provider.ErrUpstreamUnavailable
	case status >= 400:
		return provider.ErrUpstreamConfig
	}
	return nil
}
