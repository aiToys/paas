package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aitoys/paas/internal/ai/guardrail"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/tool/mcp"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/pkg/provider"
)

// tracer 是 Agent 运行的 OTel tracer（noop tracer 未接后端时无开销）。
var tracer = otel.Tracer("paas.ai/agent")

// Runtime 执行 Agent：组装 system prompt（agent.SystemPrompt 或 PromptRef 模板）+
// KB RAG 上下文 + 工具定义（FunctionCalling），调底层 LLM。
//
// 当 Agent 配置了工具时进入**多轮工具循环**（MaxSteps 上限）：
// 每轮把工具定义传给 LLM → 若 LLM 返回 tool_calls，runtime 调用对应工具
// （MCP tools/call）→ 把结果作为 role=tool 消息回喂 → 下一轮 LLM 据结果作答；
// 直到 LLM 不再请求工具（产出最终答案，流式输出），或达 MaxSteps 上限。
// 工具调用进度作为 reasoning_content 推送（前端思考面板可见）。
//
// 依赖（依赖倒置，cmd/core 注入；任一为 nil 则该维度降级跳过）：
//   - models：MaaS catalog 取底层 Provider
//   - resolver：凭证解析
//   - prompts：PromptRef 解析（agent.SystemPrompt 为空时用）
//   - tools：工具定义 + 调用（仅 MCP 类型可调用）
//   - kb：知识库 RAG 检索
type Runtime struct {
	agents   Repository
	models   maas.Repository
	resolver provider.CredentialResolver
	prompts  prompt.Repository
	tools    tool.Repository
	kb       *knowledgebase.Retriever
	guard    guardrail.Guard // 输入/输出护栏（nil 全放行）
	// promptLogEnabled 为 true 时，结构化日志记录输入/输出摘要（脱敏长度），便于审计/调试。
	promptLogEnabled bool
}

// WithGuard 注入护栏（依赖倒置；不调则全放行）。
func (r *Runtime) WithGuard(g guardrail.Guard) *Runtime { r.guard = g; return r }

// WithPromptLog 开启输入/输出摘要日志（PAAS_AI_LOG_PROMPTS=true 时调）。
func (r *Runtime) WithPromptLog(enabled bool) *Runtime { r.promptLogEnabled = enabled; return r }

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

// toolInvoker 执行单个工具调用，返回结果文本（喂给 LLM）。
// argsJSON 是 LLM 产出的函数参数 JSON 字符串（OpenAI 规范）。
type toolInvoker func(ctx context.Context, argsJSON string) (string, error)

// lastUserText 取 messages 中最后一条 user 消息文本（KB RAG query 用）。
func lastUserText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// buildSystem 组装 system prompt：agent.SystemPrompt 或 PromptRef 模板 + KB RAG 上下文。
// 工具描述不在此注入（启用工具时以结构化 ToolDef 传 LLM，见 buildTools）。
func (r *Runtime) buildSystem(ctx context.Context, a Agent, msgs []provider.Message) string {
	sys := a.SystemPrompt
	if sys == "" && a.PromptRef != "" && r.prompts != nil {
		if p, err := r.prompts.GetActive(ctx, a.PromptRef); err == nil {
			sys = p.Template
		}
	}
	// KB RAG：用最后一条 user 消息检索，注入相关切片。
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

// buildTools 从 agent 引用的工具实体构建 LLM 工具定义 + 调用器。
// 仅 MCP 类型可调用：经 ListTools 取真实 JSON Schema 参数；按工具实体 Name 匹配 MCP 工具名
// （匹配失败取首个），invoker 调 tools/call。http/builtin 暂不可调用（invoke 仅 mcp，留后续）。
func (r *Runtime) buildTools(ctx context.Context, a Agent) ([]provider.ToolDef, map[string]toolInvoker) {
	if r.tools == nil || len(a.Tools) == 0 {
		return nil, nil
	}
	var defs []provider.ToolDef
	invokers := map[string]toolInvoker{}
	for _, tid := range a.Tools {
		t, err := r.tools.Get(ctx, tid)
		if err != nil || !t.Enabled || t.Type != tool.TypeMCP {
			continue
		}
		serverURL := t.Config[tool.CfgMCPServerURL]
		apiKey := t.Config[tool.CfgMCPAPIKey]
		if serverURL == "" {
			continue
		}
		cli := mcp.GetClient(serverURL, apiKey)
		// Initialize + ListTools 取真实 schema（失败则降级 permissive schema，仍可调）。
		mcpName := t.Name
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if err := cli.Initialize(ctx); err == nil {
			if tools, err := cli.ListTools(ctx); err == nil {
				for _, mt := range tools {
					if mt.Name == t.Name || mcpName == t.Name {
						mcpName = mt.Name
						if mt.InputSchema != nil {
							schema = mt.InputSchema
						}
						break
					}
				}
			}
		}
		fnName := t.Name // LLM 侧函数名 = 工具实体名（租户内唯一，避免冲突）
		defs = append(defs, provider.ToolDef{
			Type: "function",
			Function: provider.ToolDefFunction{
				Name: fnName, Description: t.Description, Parameters: schema,
			},
		})
		// 捕获 mcpName（可能被 ListTools 改写）进闭包。
		invokeMcpName := mcpName
		cliRef := cli
		invokers[fnName] = func(c context.Context, argsJSON string) (string, error) {
			args := map[string]any{}
			if argsJSON != "" {
				_ = json.Unmarshal([]byte(argsJSON), &args) // 解析失败按空参调（permissive schema 容错）
			}
			res, err := cliRef.Invoke(c, invokeMcpName, args)
			if err != nil {
				return "", err
			}
			// 拼 content[].text 为结果文本（MCP 结果是多块 content）。
			var sb strings.Builder
			for _, c := range res.Content {
				if c.Text != "" {
					sb.WriteString(c.Text)
				}
			}
			return sb.String(), nil
		}
	}
	return defs, invokers
}

// Run 执行 Agent，流式回调 onChunk（content/reasoning + 工具进度）。
// 启用工具时进入多轮循环；底层 LLM 不可达（凭证/模型缺失）返脱敏错误。
// 输入护栏在调 LLM 前拦截（命中返 ErrBlocked）；输出护栏逐段检（命中截断 + ErrBlocked）。
// 整轮包在 gen_ai OTel span 内（接 Jaeger 后 /api/observability/traces 可观测）。
func (r *Runtime) Run(ctx context.Context, agentID string, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	a, err := r.agents.Get(ctx, agentID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("agent 已禁用")
	}
	// 输入护栏：检查最后一条用户消息（拦截滥用/越权提示）。
	if r.guard != nil {
		if d := r.guard.CheckInput(ctx, lastUserText(msgs)); !d.Allowed {
			return fmt.Errorf("%w: %s", guardrail.ErrBlocked, d.Reason)
		}
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

	// gen_ai span：OpenTelemetry GenAI 语义约定，接 Jaeger 后可观测 Agent 链路。
	ctx, span := tracer.Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"), // semconv 值域：invoke_agent（非 "agent"）
			attribute.String("gen_ai.system", "paas"),
			attribute.String("gen_ai.request.model", a.Model),
			attribute.String("gen_ai.agent.id", agentID),
		),
	)
	defer span.End()
	if r.promptLogEnabled {
		log.Printf("[ai] agent=%s model=%s inputChars=%d", agentID, a.Model, totalChars(msgs)) //nolint:gosec // agentID/模型名来自 admin 配置实体
	}

	err = r.runLoop(ctx, p, a, msgs, onChunk)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.SetAttributes(attribute.Int("gen_ai.agent.max_steps", a.MaxSteps))
	return err
}

// CheckInput 仅做输入护栏预检（不调 LLM），供 handler 在开 SSE 前返回干净 422。
// 无护栏或通过返 nil；命中返 wrapped ErrBlocked。Run 内部也会再检一次（覆盖不经 handler 的
// gateway agent:{id} 路径），双重保险。
func (r *Runtime) CheckInput(ctx context.Context, agentID string, msgs []provider.Message) error {
	if r.guard == nil {
		return nil
	}
	a, err := r.agents.Get(ctx, agentID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("agent 已禁用")
	}
	if d := r.guard.CheckInput(ctx, lastUserText(msgs)); !d.Allowed {
		return fmt.Errorf("%w: %s", guardrail.ErrBlocked, d.Reason)
	}
	return nil
}

// totalChars 统计消息总字符数（prompt 日志摘要用，不含完整内容防泄漏）。
func totalChars(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
	}
	return n
}

// runLoop 跑多轮工具循环（抽离自 Run 便于用 fake provider 单测）。
// p 为底层 LLM Provider；a.MaxSteps 为轮数上限。
func (r *Runtime) runLoop(ctx context.Context, p provider.Provider, a Agent, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	// 组装对话上下文：system + 用户消息。
	sys := r.buildSystem(ctx, a, msgs)
	conv := make([]provider.Message, 0, len(msgs)+1)
	if sys != "" {
		conv = append(conv, provider.Message{Role: "system", Content: sys})
	}
	conv = append(conv, msgs...)

	// 工具定义 + 调用器（无工具时单轮直出）。
	defs, invokers := r.buildTools(ctx, a)

	for step := 1; step <= a.MaxSteps; step++ {
		req := provider.ChatRequest{Model: a.Model, Messages: conv, Stream: true}
		if len(defs) > 0 {
			req.Tools = defs
			req.ToolChoice = "auto"
		}
		chunkCh, err := p.Chat(ctx, req)
		if err != nil {
			return err
		}
		var assistantText strings.Builder
		var toolCalls []provider.ToolCall
		for c := range chunkCh {
			if c.Role != "" || c.Content != "" || c.Reasoning != "" {
				// 输出护栏：逐段检生成内容（命中即截断 + ErrBlocked，停止后续输出）。
				if r.guard != nil && c.Content != "" {
					if d := r.guard.CheckOutput(ctx, c.Content); !d.Allowed {
						return fmt.Errorf("%w: %s", guardrail.ErrBlocked, d.Reason)
					}
				}
				onChunk(c)
				if c.Content != "" {
					assistantText.WriteString(c.Content)
				}
			}
			if len(c.ToolCalls) > 0 {
				toolCalls = c.ToolCalls
			}
		}
		// 无工具调用：本轮 content 即最终答案（已流式输出），结束。
		if len(toolCalls) == 0 {
			if r.promptLogEnabled {
				log.Printf("[ai] agent=%s 输出字符数=%d", a.ID, assistantText.Len())
			}
			return nil
		}
		// 有工具调用：回放 assistant 消息（含 tool_calls），执行工具，追加 role=tool 结果。
		conv = append(conv, provider.Message{Role: "assistant", Content: assistantText.String(), ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			inv, ok := invokers[tc.Function.Name]
			result := ""
			if !ok {
				result = "未知工具: " + tc.Function.Name
				onChunk(provider.Chunk{Reasoning: fmt.Sprintf("\n⚠️ 未知工具 %s\n", tc.Function.Name)})
			} else {
				onChunk(provider.Chunk{Reasoning: fmt.Sprintf("\n🔧 调用工具 %s(%s)\n", tc.Function.Name, tc.Function.Arguments)})
				// gen_ai tool span：标记一次工具调用（trace 树 agent.run → chat → tool.call）。
				toolCtx, toolSpan := tracer.Start(ctx, "tool.call",
					trace.WithAttributes(attribute.String("gen_ai.tool.name", tc.Function.Name)),
				)
				if res, err := inv(toolCtx, tc.Function.Arguments); err == nil {
					result = res
				} else {
					result = "工具调用失败: " + err.Error()
					toolSpan.SetStatus(codes.Error, err.Error())
				}
				toolSpan.End()
				onChunk(provider.Chunk{Reasoning: "结果: " + truncate(result, 500) + "\n"})
			}
			conv = append(conv, provider.Message{Role: "tool", Content: result, ToolCallID: tc.ID})
		}
	}
	// 达 MaxSteps 上限：最后一轮 content 已流式输出，正常结束。
	return nil
}

// truncate 截断长文本（工具结果入 reasoning 面板用，防刷屏）。
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
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
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
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
