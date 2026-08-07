package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/pkg/provider"
)

// Runtime 执行 Agent：组装 system prompt（agent.SystemPrompt 或 PromptRef 模板）+
// 工具描述（Tools 引用）+ KB RAG 上下文（KnowledgeBases 检索），调底层 LLM 流式输出。
//
// 依赖（依赖倒置，cmd/core 注入；任一为 nil 则该维度降级跳过）：
//   - models：MaaS catalog 取底层 Provider
//   - resolver：凭证解析
//   - prompts：PromptRef 解析（agent.SystemPrompt 为空时用）
//   - tools：工具描述注入
//   - kb：知识库 RAG 检索
//
// FunctionCalling 多轮 tool 调用循环留下一 P3 切片（当前仅描述注入 + 单轮 LLM）。
type Runtime struct {
	agents   Repository
	models   maas.Repository
	resolver provider.CredentialResolver
	prompts  prompt.Repository
	tools    tool.Repository
	kb       *knowledgebase.Retriever
}

func NewRuntime(
	agents Repository,
	models maas.Repository,
	resolver provider.CredentialResolver,
	prompts prompt.Repository,
	tools tool.Repository,
	kb *knowledgebase.Retriever,
) *Runtime {
	return &Runtime{agents: agents, models: models, resolver: resolver, prompts: prompts, tools: tools, kb: kb}
}

// lastUserText 取 messages 中最后一条 user 消息文本（KB RAG query 用）。
func lastUserText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// buildSystem 组装 system prompt：agent.SystemPrompt 或 PromptRef 模板 + 工具描述 + KB RAG 上下文。
func (r *Runtime) buildSystem(ctx context.Context, a Agent, msgs []provider.Message) string {
	sys := a.SystemPrompt
	if sys == "" && a.PromptRef != "" && r.prompts != nil {
		if p, err := r.prompts.GetActive(ctx, a.PromptRef); err == nil {
			sys = p.Template
		}
	}
	// 工具描述注入（仅描述，供 LLM 参考；FunctionCalling 执行留下一切片）
	if len(a.Tools) > 0 && r.tools != nil {
		var desc []string
		for _, tid := range a.Tools {
			if t, err := r.tools.Get(ctx, tid); err == nil && t.Enabled {
				desc = append(desc, fmt.Sprintf("- %s: %s", t.Name, t.Description))
			}
		}
		if len(desc) > 0 {
			if sys != "" {
				sys += "\n\n"
			}
			sys += "你可使用以下工具（描述供参考）:\n" + strings.Join(desc, "\n")
		}
	}
	// KB RAG：用最后一条 user 消息检索，注入相关切片
	if len(a.KnowledgeBases) > 0 && r.kb != nil {
		query := lastUserText(msgs)
		if query != "" {
			var parts []string
			for _, kbID := range a.KnowledgeBases {
				hits, err := r.kb.Retrieve(ctx, kbID, query)
				if err != nil {
					continue
				}
				for _, h := range hits {
					parts = append(parts, h.Chunk.Content)
				}
			}
			if len(parts) > 0 {
				if sys != "" {
					sys += "\n\n"
				}
				sys += "参考资料（来自知识库，请据此回答）:\n" + strings.Join(parts, "\n---\n")
			}
		}
	}
	return sys
}

// Run 执行 Agent，流式回调 onChunk。底层 LLM 不可达（凭证/模型缺失）返脱敏错误。
func (r *Runtime) Run(ctx context.Context, agentID string, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	a, err := r.agents.Get(ctx, agentID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("agent 已禁用")
	}
	// 取底层 Provider（MaaS catalog）
	m, err := r.models.GetModel(ctx, a.Model)
	if err != nil {
		return fmt.Errorf("底层模型 %s 不存在", a.Model)
	}
	if len(m.Channels) == 0 {
		return fmt.Errorf("模型 %s 无通道", a.Model)
	}
	p := maas.BuildProvider(m.Channels[0], r.resolver)

	sys := r.buildSystem(ctx, a, msgs)
	full := make([]provider.Message, 0, len(msgs)+1)
	if sys != "" {
		full = append(full, provider.Message{Role: "system", Content: sys})
	}
	full = append(full, msgs...)

	chunkCh, err := p.Chat(ctx, provider.ChatRequest{Model: a.Model, Messages: full, Stream: true})
	if err != nil {
		return err
	}
	for chunk := range chunkCh {
		onChunk(chunk)
	}
	return nil
}

// ServeSSE 把 Agent 运行以 OpenAI 兼容 SSE 输出（data: {choices:[{delta:{content}}]}）。
// 供 handler /run 端点复用 gateway 流式协议（前端 Playground 可直接消费）。
func (r *Runtime) ServeSSE(w http.ResponseWriter, ctx context.Context, agentID string, msgs []provider.Message) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	writeSSE := func(payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	writeSSE(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"role": "assistant"}}}})
	err := r.Run(ctx, agentID, msgs, func(c provider.Chunk) {
		if c.Content == "" && c.Reasoning == "" {
			return
		}
		delta := map[string]string{}
		if c.Content != "" {
			delta["content"] = c.Content
		}
		if c.Reasoning != "" {
			delta["reasoning_content"] = c.Reasoning
		}
		writeSSE(map[string]any{"choices": []map[string]any{{"delta": delta}}})
	})
	writeSSE("[DONE]")
	return err
}
