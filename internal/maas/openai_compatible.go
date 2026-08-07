package maas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/provider"
)

// readBodySnippet 读取响应体前 512 字节作诊断片段。
// 上游错误 JSON 一般不含平台凭证（凭证在请求头），可安全入日志/错误，
// 帮助定位 4xx 根因（端点 404 / 模型名 400 / 请求格式 422 等）。
func readBodySnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

// openaiReq 是 OpenAI 兼容协议的请求体（仅取推理所需字段）。
// DeepSeek / 通义千问 DashScope 兼容模式 / OpenAI 三家同构。
type openaiReq struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Stream      bool               `json:"stream"`
	Tools       []provider.ToolDef `json:"tools,omitempty"`
	ToolChoice  string             `json:"tool_choice,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	MaxTokens   *int               `json:"max_tokens,omitempty"`
}

// openaiToolCallDelta 是流式 tool_call 增量（按 index 聚合）。
// 首块带 id/type/function.name，后续块仅 function.arguments 增量拼接。
type openaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// openaiDelta 是流式增量响应（仅取 choices[0].delta）。
// ReasoningContent 是推理模型（DeepSeek-R1/GLM/QwQ/Doubao 等）的思考过程增量，
// 与 Content 正交：思考阶段 content=null + reasoning_content=...，答案阶段反之。
// 不解析则思考过程被丢弃，前端长时间空白「不像流式」。
type openaiDelta struct {
	Choices []struct {
		Delta struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openaiToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
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
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
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
		snippet := readBodySnippet(resp.Body)
		_ = resp.Body.Close()
		log.Printf("[maas] chat 上游 %s model=%s 返回 %d: %s", p.vendor, p.upstreamModel, resp.StatusCode, snippet)
		return nil, fmt.Errorf("%w: %s %d: %s", classifyErr(nil, resp.StatusCode), p.vendor, resp.StatusCode, snippet)
	}

	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		// tool_call 按 index 累积：首 delta 带 id/name，后续 delta 仅追加 arguments 片段。
		toolAcc := map[int]*provider.ToolCall{}
		finishReason := ""
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 容纳长 SSE 行
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var d openaiDelta
			if json.Unmarshal([]byte(payload), &d) != nil {
				continue // 跳过无法解析的行（如上游心跳/注释）
			}
			if len(d.Choices) == 0 {
				continue
			}
			choice := d.Choices[0]
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			delta := choice.Delta
			// 累积 tool_calls（按 index，跨 delta 拼接 arguments）。
			for _, tc := range delta.ToolCalls {
				cur, ok := toolAcc[tc.Index]
				if !ok {
					cur = &provider.ToolCall{Type: "function"}
					toolAcc[tc.Index] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name = tc.Function.Name
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
			if delta.Role == "" && delta.Content == "" && delta.ReasoningContent == "" && len(delta.ToolCalls) == 0 {
				continue
			}
			// content/reasoning 流式透传（tool_calls 增量不单独推，流末统一发）。
			if delta.Role != "" || delta.Content != "" || delta.ReasoningContent != "" {
				select {
				case <-ctx.Done():
					return // 客户端断连，立即停止解析（不计费、不泄漏 goroutine）
				case ch <- provider.Chunk{Role: delta.Role, Content: delta.Content, Reasoning: delta.ReasoningContent}:
				}
			}
		}
		// 流末：若 LLM 决定调用工具，发一个聚合 Chunk（tool_calls + finish_reason）供 runtime 续轮。
		if len(toolAcc) > 0 {
			calls := make([]provider.ToolCall, 0, len(toolAcc))
			// 按 index 稳定排序，保证回放顺序确定。
			idxs := make([]int, 0, len(toolAcc))
			for i := range toolAcc {
				idxs = append(idxs, i)
			}
			sort.Ints(idxs)
			for _, i := range idxs {
				calls = append(calls, *toolAcc[i])
			}
			if finishReason == "" {
				finishReason = "tool_calls"
			}
			select {
			case <-ctx.Done():
				return
			case ch <- provider.Chunk{ToolCalls: calls, FinishReason: finishReason}:
			}
		}
	}()
	return ch, nil
}

// openaiEmbedReq 是 OpenAI 兼容 /embeddings 请求体。
type openaiEmbedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// openaiEmbedResp 是 /embeddings 响应。data 按 index 排序后取 embedding，
// 确保返回顺序与输入 texts 一致（部分供应商不保证 data 数组顺序）。
type openaiEmbedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// embedBatchSize 是单次 /embeddings 请求的 input 上限。
// airouter/百炼 text-embedding-v4 限制 batch size ≤ 10（超限返 400 InvalidParameter:
// "batch size is invalid, it should not be larger than 10"）。上游限制可能因模型版本/
// 路由实例动态变化（曾观察到 20 与 10 两种），取最保守值 10 保证稳定。大文档切片数常 > 10，
// 必须分批调用后合并结果，否则 embedding 整体失败致文档卡 failed。
const embedBatchSize = 10

// Embed 调供应商 /embeddings 批量向量化（非流式）。失败按 sentinel 分类，
// 与 Chat 同款（凭证缺失/offline/degraded），便于 KB 降级决策。
//
// 分批：texts 超过 embedBatchSize 时按批调用，结果按 index 合并（上游返回的 index 是
// 批内相对位置，回填全局时加 offset）。任一批失败即整体失败（fail-fast，不部分入库）。
func (p *OpenAICompatibleProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p.resolver == nil {
		return nil, provider.ErrCredentialMissing
	}
	apiKey, err := p.resolver.Resolve(p.credentialRef)
	if err != nil || apiKey == "" {
		return nil, provider.ErrCredentialMissing
	}
	if len(texts) == 0 {
		return nil, nil
	}

	out := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		if err := p.embedBatch(ctx, apiKey, texts[start:end], out, start); err != nil {
			return nil, err
		}
	}
	// 校验：每个输入都有对应向量（缺则视为上游不可用）
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("%w: embedding 响应缺失 index=%d", provider.ErrUpstreamUnavailable, i)
		}
	}
	return out, nil
}

// embedBatch 调用一次 /embeddings，把批内结果按 offset 回填到全局 out。
func (p *OpenAICompatibleProvider) embedBatch(ctx context.Context, apiKey string, batch []string, out [][]float32, offset int) error {
	body, _ := json.Marshal(openaiEmbedReq{Model: p.upstreamModel, Input: batch})
	endpoint := strings.TrimRight(p.baseURL, "/") + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return classifyErr(err, 0)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		log.Printf("[maas] embedding 上游 %s model=%s 返回 %d: %s", p.vendor, p.upstreamModel, resp.StatusCode, snippet)
		return fmt.Errorf("%w: %s %d: %s", classifyErr(nil, resp.StatusCode), p.vendor, resp.StatusCode, snippet)
	}
	var r openaiEmbedResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("%w: 解析 embedding 响应失败: %v", provider.ErrUpstreamUnavailable, err)
	}
	// 上游返回的 index 是批内相对位置（0..len(batch)-1），回填全局 out 时加 offset。
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(batch) {
			continue
		}
		out[offset+d.Index] = d.Embedding
	}
	return nil
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
