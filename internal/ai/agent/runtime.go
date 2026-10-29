package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/aitoys/paas/internal/ai/skill"
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
// （MCP tools/call 或内置工具进程内直调）→ 把结果作为 role=tool 消息回喂 → 下一轮 LLM 据结果作答；
// 直到 LLM 不再请求工具（产出最终答案，流式输出），或达 MaxSteps 上限。
// 工具调用进度作为 reasoning_content 推送（前端思考面板可见）。
//
// 依赖（依赖倒置，cmd/core 注入；任一为 nil 则该维度降级跳过）：
//   - models：MaaS catalog 取底层 Provider
//   - resolver：凭证解析
//   - prompts：PromptRef 解析（agent.SystemPrompt 为空时用）
//   - tools：工具定义 + 调用（MCP / builtin 类型可调用）
//   - kb：知识库 RAG 检索
//   - diag：内置工具集（平台诊断等 super_admin 语境；nil 则 builtin 实体不可用）
type Runtime struct {
	agents   Repository
	models   maas.Repository
	resolver provider.CredentialResolver
	prompts  prompt.Repository
	tools    tool.Repository
	skills   skill.Repository
	kb       *knowledgebase.Retriever
	guard    guardrail.Guard // 输入/输出护栏（nil 全放行）
	diag     DiagnosticToolset
	// promptLogEnabled 为 true 时，结构化日志记录输入/输出摘要（脱敏长度），便于审计/调试。
	promptLogEnabled bool
	// MeterFn token 用量记账钩子（cmd/core 注入 gateway.Meter.Record，粗估口径与 gateway stream 一致）。
	// 覆盖不经 gateway 的三条推理路径（/api/agents/{id}/run、eval、workflow llm 节点），防绕过 billing 配额。nil 则不记。
	MeterFn func(ctx context.Context, agentID string, tokens int)
}

// DiagnosticToolset 内置工具集契约（依赖倒置；实现方 internal/diagnose/tools）。
//
// 语境约束：内置工具面向**平台运维**（super_admin 诊断等），按原始 (namespace, pod)
// 寻址、不经租户 scope——绝不能让租户 agent 经 builtin 工具实体触达（越权读任意 ns）。
// 闸门：buildTools 的 builtin 分支要求 ctx 带诊断标志（diagnoseCtxKey，仅 RunDiagnose
// 内部设置），租户路径（RunConv）永远无标志，builtin 实体在租户语境一律拒绝。
type DiagnosticToolset interface {
	// Handlers 返回全部内置工具 handler 名（注册表键集，稳定顺序）。
	Handlers() []string
	// Lookup 按 handler 名取工具元信息 + 进程内调用器。
	// argsJSON 是 LLM 产出的函数参数 JSON 字符串（OpenAI 规范）；返回结果文本喂 LLM。
	Lookup(handler string) (meta BuiltinToolMeta, invoke func(ctx context.Context, argsJSON string) (string, error), ok bool)
}

// BuiltinToolMeta 内置工具的 LLM 侧定义（Description 进 ToolDef；Schema 为 JSON Schema 参数）。
type BuiltinToolMeta struct {
	Description string
	Schema      map[string]any
}

// WithSkills 注入 skill 仓储（依赖倒置；不调则 skill 维度降级跳过）。
func (r *Runtime) WithSkills(s skill.Repository) *Runtime { r.skills = s; return r }

// WithDiagnostics 注入内置工具集（依赖倒置；不调则 builtin 工具不可用）。
func (r *Runtime) WithDiagnostics(d DiagnosticToolset) *Runtime { r.diag = d; return r }

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

type dependencyFailure struct{ reason string }

func (e *dependencyFailure) Error() string { return e.reason }
func dependencyErrorf(format string, args ...any) error {
	return &dependencyFailure{reason: fmt.Sprintf(format, args...)}
}

// buildSystem 组装 system prompt：agent.SystemPrompt 或 PromptRef 模板 + Skill 能力指令 + KB RAG 上下文。
// 工具描述不在此注入（启用工具时以结构化 ToolDef 传 LLM，见 buildTools）。
// 引用的资产均是显式依赖；失败时终止并说明，避免无知识/工具仍返回成功。
func (r *Runtime) buildSystem(ctx context.Context, a Agent, msgs []provider.Message) (string, error) {
	sys := a.SystemPrompt
	if sys == "" && a.PromptRef != "" {
		if r.prompts == nil {
			return "", dependencyErrorf("提示词服务未接入")
		}
		p, err := r.prompts.GetActive(ctx, a.PromptRef)
		if err != nil {
			return "", dependencyErrorf("引用的提示词不可用，请检查激活版本")
		}
		if len(p.Variables) > 0 || strings.Contains(p.Template, "{{") {
			return "", dependencyErrorf("提示词含未绑定变量，当前智能体仅支持静态系统提示；请将变量填写为静态值后再运行")
		}
		sys = p.Template
	}
	for i, sid := range a.Skills {
		instructions, err := r.loadSkillInstructions(ctx, a, sid)
		if err != nil {
			return "", err
		}
		if sys != "" {
			sys += "\n\n"
		}
		sys += instructions
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("paas.ai.skills.loaded_count", i+1))
	}
	if len(a.KnowledgeBases) > 0 {
		if r.kb == nil {
			return "", dependencyErrorf("知识库检索服务未接入")
		}
		query := lastUserText(msgs)
		if query == "" {
			return "", dependencyErrorf("知识库检索需要用户输入")
		}
		var parts []string
		for _, id := range a.KnowledgeBases {
			hits, err := r.kb.Retrieve(ctx, id, query)
			if err != nil {
				return "", dependencyErrorf("知识库检索失败，请检查索引与数据服务后重试")
			}
			for _, h := range hits {
				parts = append(parts, h.Chunk.Content)
			}
		}
		if len(parts) > 0 {
			sys += "\n\n参考资料（来自知识库，请据此回答）:\n" + strings.Join(parts, "\n---\n")
		}
	}
	return sys, nil
}

// buildTools 从 agent 引用的工具实体构建 LLM 工具定义 + 调用器。
//   - MCP：经 ListTools 取真实 JSON Schema 参数；按工具实体 Name 精确匹配 MCP 工具名
//     （匹配失败保持实体名直调，MCP 端报 unknown tool），invoker 调 tools/call。
//   - builtin：按 Config["handler"] 查注入的 DiagnosticToolset 进程内直调（完成 TypeBuiltin
//     设计留白）。**仅诊断语境可用**（ctx 带诊断标志）：内置工具按原始 (ns, pod) 寻址、
//     不经租户 scope，租户 agent 经 builtin 实体触达即越权读任意 ns，一律拒绝。
//   - http 暂不可调用（留后续）。
func (r *Runtime) buildTools(ctx context.Context, a Agent) ([]provider.ToolDef, map[string]toolInvoker, error) {
	if len(a.Tools) == 0 {
		return nil, nil, nil
	}
	if r.tools == nil {
		return nil, nil, dependencyErrorf("工具服务未接入")
	}
	var defs []provider.ToolDef
	invokers := map[string]toolInvoker{}
	for _, tid := range a.Tools {
		t, err := r.tools.Get(ctx, tid)
		if err != nil || !t.Enabled {
			return nil, nil, dependencyErrorf("引用的工具不可用或已禁用")
		}
		if t.Type == tool.TypeBuiltin {
			if r.diag == nil || ctx.Value(diagnoseCtxKey{}) == nil {
				return nil, nil, dependencyErrorf("工具 %s 为平台内置工具，仅限平台诊断使用", t.Name)
			}
			handler := t.Config["handler"]
			meta, invoke, ok := r.diag.Lookup(handler)
			if !ok {
				return nil, nil, dependencyErrorf("内置工具 %s 未注册", handler)
			}
			fnName := t.Name // LLM 侧函数名 = 工具实体名（租户内唯一，避免冲突）
			defs = append(defs, provider.ToolDef{
				Type: "function",
				Function: provider.ToolDefFunction{
					Name: fnName, Description: t.Description, Parameters: meta.Schema,
				},
			})
			invokers[fnName] = invoke
			continue
		}
		if t.Type != tool.TypeMCP {
			return nil, nil, dependencyErrorf("工具 %s 暂不支持智能体调用，仅支持 MCP", t.Name)
		}
		serverURL := t.Config[tool.CfgMCPServerURL]
		apiKey := t.Config[tool.CfgMCPAPIKey]
		if serverURL == "" {
			return nil, nil, dependencyErrorf("工具 %s 未配置服务地址", t.Name)
		}
		cli := mcp.GetClient(serverURL, apiKey)
		// Initialize + ListTools 取真实 schema（失败则降级 permissive schema，仍可调）。
		// 按工具实体名精确匹配 MCP 工具名；匹配失败保持实体名直调（MCP 端报 unknown tool，
		// 调用方可见真实错误）。（R11 审计 C1：原条件 `mt.Name == t.Name || mcpName == t.Name`
		// 第二项在首轮恒真——任何实体都被改写为调 MCP server 的**第一个**工具，
		// 多工具 server 下全部串调 tools[0]，schema 也错位。）
		mcpName := t.Name
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if err := cli.Initialize(ctx); err != nil {
			return nil, nil, dependencyErrorf("工具 %s 连接失败，请检查地址与凭证", t.Name)
		}
		methods, err := cli.ListTools(ctx)
		if err != nil {
			return nil, nil, dependencyErrorf("工具 %s 方法发现失败", t.Name)
		}
		matched := false
		for _, method := range methods {
			if method.Name == t.Name {
				matched = true
				if method.InputSchema != nil {
					schema = method.InputSchema
				}
				break
			}
		}
		if !matched {
			return nil, nil, dependencyErrorf("工具 %s 未在 MCP 服务暴露，请选择实际方法", t.Name)
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
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", dependencyErrorf("工具参数不是合法 JSON")
				} // 解析失败按空参调（permissive schema 容错）
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
	return defs, invokers, nil
}

// Run 执行 Agent，流式回调 onChunk（content/reasoning + 工具进度）。
// 启用工具时进入多轮循环；底层 LLM 不可达（凭证/模型缺失）返脱敏错误。
// 输入护栏在调 LLM 前拦截（命中返 ErrBlocked）；输出护栏逐段检（命中截断 + ErrBlocked）。
// 整轮包在 gen_ai OTel span 内（接 Jaeger 后 /api/observability/traces 可观测）。
// Run 运行 Agent。conversationId 非空时启用多轮记忆：历史前置 + 本轮追加（内存环形，见 memory.go）。
func (r *Runtime) Run(ctx context.Context, agentID string, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	return r.RunConv(ctx, agentID, "", msgs, onChunk)
}

// RunConv 带会话 ID 的运行（conversationId 空 = 无状态单轮，现状不变）。
func (r *Runtime) RunConv(ctx context.Context, agentID, conversationID string, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	a, err := r.agents.Get(ctx, agentID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("agent 已禁用")
	}
	// 多轮记忆：conversationId 非空时前置历史（不含 system——runLoop 每轮重建）。
	if conversationID != "" {
		msgs = append(conversations.loadHistory(agentID, conversationID), msgs...)
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
			attribute.String("gen_ai.agent.name", a.Name),
		),
	)
	defer span.End()
	if r.promptLogEnabled {
		log.Printf("[ai] agent=%s model=%s inputChars=%d", agentID, a.Model, totalChars(msgs)) //nolint:gosec // agentID/模型名来自 admin 配置实体
	}

	// 收集本轮 assistant 输出（会话记忆追加用；包装 onChunk 不影响流式回调）
	var finalText strings.Builder
	wrapped := func(c provider.Chunk) {
		if c.Content != "" {
			finalText.WriteString(c.Content)
		}
		if onChunk != nil {
			onChunk(c)
		}
	}
	err = r.runLoop(ctx, p, a, msgs, wrapped)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	// token 计量（rune 粗估：输入 + 输出，gateway stream 同口径）
	if r.MeterFn != nil {
		r.MeterFn(ctx, agentID, totalChars(msgs)+finalText.Len())
	}
	span.SetAttributes(attribute.Int("gen_ai.agent.max_steps", a.MaxSteps))
	// 多轮记忆：成功结束后追加本轮 user + assistant（user 取原始最后一条）
	if conversationID != "" && len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == "user" {
			conversations.appendHistory(agentID, conversationID, last, provider.Message{Role: "assistant", Content: finalText.String()})
		}
	}
	return nil
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

// diagnoseCtxKey 诊断语境标志（未导出结构体 key：只有本包 RunDiagnose 能设置，
// 租户路径无法伪造——buildTools builtin 分支据此拒绝租户 agent 触达内置工具）。
type diagnoseCtxKey struct{}

// RunDiagnose 跑平台诊断 agent（super_admin 语境，虚拟 agent 非租户实体）：
// 工具集全量来自注入的 DiagnosticToolset（按原始 (ns, pod) 寻址，不经租户 scope、
// 不碰 agents/tools repository），模型/SystemPrompt/MaxSteps 由 a 指定
// （cmd/core 装配层从 env + 诊断预设构造）。复用 toolLoop 多轮循环 +
// OTel gen_ai span + token 计量；不走会话记忆（无状态单次诊断）。
func (r *Runtime) RunDiagnose(ctx context.Context, a Agent, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	if r.diag == nil {
		return dependencyErrorf("诊断工具集未注入")
	}
	if a.MaxSteps <= 0 {
		a.MaxSteps = DefaultMaxSteps
	}
	// 取底层 Provider（MaaS catalog；与 RunConv 同源，模型不存在时脱敏报错）。
	m, err := r.models.GetModel(ctx, a.Model)
	if err != nil {
		return fmt.Errorf("底层模型 %s 不存在", a.Model)
	}
	if len(m.Channels) == 0 {
		return fmt.Errorf("模型 %s 无通道", a.Model)
	}
	p := maas.BuildProvider(m.Channels[0], r.resolver)
	// 诊断语境标志：builtin 工具分支唯一放行口（见 diagnoseCtxKey 注释）。
	ctx = context.WithValue(ctx, diagnoseCtxKey{}, true)
	ctx, span := tracer.Start(ctx, "agent.diagnose",
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.system", "paas"),
			attribute.String("gen_ai.request.model", a.Model),
			attribute.String("gen_ai.agent.name", a.Name),
			attribute.Int("gen_ai.agent.max_steps", a.MaxSteps),
		),
	)
	defer span.End()
	// 全量内置工具（handler 名即 LLM 函数名；进程内注册表天然无重名）。
	var defs []provider.ToolDef
	invokers := map[string]toolInvoker{}
	for _, h := range r.diag.Handlers() {
		meta, invoke, ok := r.diag.Lookup(h)
		if !ok {
			continue // 注册表静态不变，此分支仅为防御
		}
		defs = append(defs, provider.ToolDef{
			Type:     "function",
			Function: provider.ToolDefFunction{Name: h, Description: meta.Description, Parameters: meta.Schema},
		})
		invokers[h] = invoke
	}
	conv := make([]provider.Message, 0, len(msgs)+1)
	if a.SystemPrompt != "" {
		conv = append(conv, provider.Message{Role: "system", Content: a.SystemPrompt})
	}
	conv = append(conv, msgs...)
	var finalText strings.Builder
	wrapped := func(c provider.Chunk) {
		if c.Content != "" {
			finalText.WriteString(c.Content)
		}
		if onChunk != nil {
			onChunk(c)
		}
	}
	if _, err := r.toolLoop(ctx, p, a, conv, defs, invokers, wrapped, nil); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if r.MeterFn != nil {
		r.MeterFn(ctx, a.ID, totalChars(msgs)+finalText.Len())
	}
	return nil
}

// runLoop 跑多轮工具循环（抽离自 Run 便于用 fake provider 单测）。
// p 为底层 LLM Provider；a.MaxSteps 为轮数上限。
func (r *Runtime) runLoop(ctx context.Context, p provider.Provider, a Agent, msgs []provider.Message, onChunk func(provider.Chunk)) error {
	runSpan := trace.SpanFromContext(ctx)
	runSpan.SetAttributes(
		attribute.Int("paas.ai.skills.requested_count", len(a.Skills)),
		attribute.Int("paas.ai.skills.loaded_count", 0),
		attribute.Bool("paas.ai.context.ready", false),
	)
	// 组装对话上下文：system + 用户消息。
	sys, err := r.buildSystem(ctx, a, msgs)
	if err != nil {
		return err
	}
	conv := make([]provider.Message, 0, len(msgs)+1)
	if sys != "" {
		conv = append(conv, provider.Message{Role: "system", Content: sys})
	}
	conv = append(conv, msgs...)

	// 工具定义 + 调用器（无工具时单轮直出）。
	defs, invokers, err := r.buildTools(ctx, a)
	if err != nil {
		return err
	}
	runSpan.SetAttributes(attribute.Bool("paas.ai.context.ready", true))

	final, err := r.toolLoop(ctx, p, a, conv, defs, invokers, onChunk, nil)
	_ = final // 普通路径不需要终稿轮文本（流式已输出）
	return err
}

// ToolHooks 工具执行确定性钩子（spec §5.1：进度事件由 hook 发出，不从
// Chunk.Reasoning 解析）。单次调用参数，不挂在共享 Runtime 上。
type ToolHooks struct {
	OnToolStart  func(name string)            // 工具开始（发 tool.started 事件）
	OnToolEnd    func(name string, err error) // 工具结束（err=nil 成功）
	BeforeModel  func(ctx context.Context, reserve int) (func(provider.Usage, string), error)
	BeforeTool   func(ctx context.Context) error
	MaxToolCalls int
	NoTools      bool
}

// RunScoped 按次注入工具集的受控诊断执行入口（spec §5.1 RunWithToolset）。
// 与 RunDiagnose 的差异：ts 由调用方单次传入（用户侧=ScopedToolset，防并发串租户）、
// hooks 发工具进度、**返回最后一个无工具调用轮次的 assistant 终稿文本**（报告解析源
// ——中间轮说明文本不混入，避免拼接全部 Content 污染 JSON）。onChunk 不需要（异步
// worker 不流式）。
func (r *Runtime) RunScoped(ctx context.Context, a Agent, msgs []provider.Message, ts DiagnosticToolset, hooks *ToolHooks) (string, error) {
	if ts == nil {
		return "", dependencyErrorf("工具集未注入")
	}
	if a.MaxSteps <= 0 {
		a.MaxSteps = DefaultMaxSteps
	}
	m, err := r.models.GetModel(ctx, a.Model)
	if err != nil {
		return "", fmt.Errorf("底层模型 %s 不存在", a.Model)
	}
	if len(m.Channels) == 0 {
		return "", fmt.Errorf("模型 %s 无通道", a.Model)
	}
	var channel *provider.Channel
	for _, candidate := range m.Channels {
		if candidate != nil && candidate.Status != "offline" && (channel == nil || candidate.Priority < channel.Priority) {
			channel = candidate
		}
	}
	if channel == nil {
		return "", dependencyErrorf("模型没有可用通道")
	}
	p := maas.BuildProvider(channel, r.resolver)
	ctx = context.WithValue(ctx, diagnoseCtxKey{}, true)
	ctx, span := tracer.Start(ctx, "agent.diagnose.scoped",
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.system", "paas"),
			attribute.String("gen_ai.request.model", a.Model),
			attribute.String("gen_ai.agent.name", a.Name),
			attribute.Int("gen_ai.agent.max_steps", a.MaxSteps),
		),
	)
	defer span.End()
	var defs []provider.ToolDef
	invokers := map[string]toolInvoker{}
	handlers := ts.Handlers()
	if hooks != nil && hooks.NoTools {
		handlers = nil
	}
	for _, h := range handlers {
		meta, invoke, ok := ts.Lookup(h)
		if !ok {
			continue
		}
		defs = append(defs, provider.ToolDef{
			Type:     "function",
			Function: provider.ToolDefFunction{Name: h, Description: meta.Description, Parameters: meta.Schema},
		})
		invokers[h] = invoke
	}
	conv := make([]provider.Message, 0, len(msgs)+1)
	if a.SystemPrompt != "" {
		conv = append(conv, provider.Message{Role: "system", Content: a.SystemPrompt})
	}
	conv = append(conv, msgs...)
	final, err := r.toolLoop(ctx, p, a, conv, defs, invokers, nil, hooks)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return "", err
	}
	span.SetAttributes(attribute.Int("gen_ai.output.chars", len(final)))
	return final, nil
}

// toolLoop 多轮工具循环核心（runLoop/RunDiagnose 共用）：每轮把工具定义传 LLM →
// 执行 tool_calls → role=tool 回喂 → 下一轮；LLM 不再调工具即最终答案。
// 输出护栏仅租户路径生效（RunDiagnose 不注入 guard，平台内部运行）。
func (r *Runtime) toolLoop(ctx context.Context, p provider.Provider, a Agent, conv []provider.Message, defs []provider.ToolDef, invokers map[string]toolInvoker, onChunk func(provider.Chunk), hooks *ToolHooks) (string, error) {
	toolCount := 0
	for step := 1; step <= a.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req := provider.ChatRequest{Model: a.Model, Messages: conv, Stream: true}
		if len(defs) > 0 {
			req.Tools = defs
			req.ToolChoice = "auto"
		}
		var settle func(provider.Usage, string)
		if hooks != nil && hooks.BeforeModel != nil {
			maxTokens := 4096
			req.MaxTokens = &maxTokens
			raw, _ := json.Marshal(req)
			if len(raw) > 512*1024 {
				return "", fmt.Errorf("对话上下文过长，请新建会话")
			}
			var e error
			settle, e = hooks.BeforeModel(ctx, len(raw)+maxTokens)
			if e != nil {
				return "", e
			}
		}
		chunkCh, err := p.Chat(ctx, req)
		if err != nil {
			if settle != nil {
				settle(provider.Usage{}, "estimated")
			}
			return "", err
		}
		var assistantText strings.Builder
		var usage *provider.Usage
		var outputChars int
		var toolCalls []provider.ToolCall
		for c := range chunkCh {
			if c.Usage != nil {
				u := *c.Usage
				usage = &u
			}
			outputChars += len(c.Content) + len(c.Reasoning)
			if outputChars > 128*1024 {
				if settle != nil {
					settle(provider.Usage{InputTokens: totalChars(conv), OutputTokens: outputChars}, "estimated")
				}
				return "", fmt.Errorf("模型输出超过上限")
			}

			if c.Role != "" || c.Content != "" || c.Reasoning != "" {
				// 输出护栏：逐段检生成内容（命中即截断 + ErrBlocked，停止后续输出）。
				if r.guard != nil && c.Content != "" {
					if d := r.guard.CheckOutput(ctx, c.Content); !d.Allowed {
						if settle != nil {
							settle(provider.Usage{InputTokens: totalChars(conv), OutputTokens: outputChars}, "estimated")
						}
						return "", fmt.Errorf("%w: %s", guardrail.ErrBlocked, d.Reason)
					}
				}
				if onChunk != nil {
					onChunk(c)
				}
				if c.Content != "" {
					assistantText.WriteString(c.Content)
				}
			}
			if len(c.ToolCalls) > 0 {
				toolCalls = c.ToolCalls
			}
		}
		if settle != nil {
			if usage != nil {
				settle(*usage, "provider")
			} else {
				raw, _ := json.Marshal(req)
				settle(provider.Usage{InputTokens: len(raw), OutputTokens: outputChars}, "estimated")
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// 无工具调用：本轮 content 即最终答案（已流式输出），结束。
		if len(toolCalls) == 0 {
			if r.promptLogEnabled {
				log.Printf("[ai] agent=%s 输出字符数=%d", a.ID, assistantText.Len())
			}
			return assistantText.String(), nil
		}
		// 有工具调用：回放 assistant 消息（含 tool_calls），执行工具，追加 role=tool 结果。
		conv = append(conv, provider.Message{Role: "assistant", Content: assistantText.String(), ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			toolCount++
			if hooks != nil {
				if hooks.MaxToolCalls > 0 && toolCount > hooks.MaxToolCalls {
					return "", fmt.Errorf("工具调用次数已达上限")
				}
				if hooks.BeforeTool != nil {
					if err := hooks.BeforeTool(ctx); err != nil {
						return "", err
					}
				}
			}

			inv, ok := invokers[tc.Function.Name]
			result := ""
			if !ok {
				result = "未知工具: " + tc.Function.Name
				if onChunk != nil {
					onChunk(provider.Chunk{Reasoning: fmt.Sprintf("\n⚠️ 未知工具 %s\n", tc.Function.Name)})
				}
			} else {
				if onChunk != nil {
					onChunk(provider.Chunk{Reasoning: fmt.Sprintf("\n🔧 调用工具 %s(%s)\n", tc.Function.Name, tc.Function.Arguments)})
				}
				if hooks != nil && hooks.OnToolStart != nil {
					hooks.OnToolStart(tc.Function.Name)
				}
				// gen_ai tool span：标记一次工具调用（trace 树 agent.run → chat → tool.call）。
				toolCtx, toolSpan := tracer.Start(ctx, "tool.call",
					trace.WithAttributes(
						attribute.String("gen_ai.operation.name", "execute_tool"),
						attribute.String("gen_ai.tool.name", tc.Function.Name),
					),
				)
				if tc.ID != "" {
					toolSpan.SetAttributes(attribute.String("gen_ai.tool.call.id", tc.ID))
				}
				var invErr error
				if res, err := inv(toolCtx, tc.Function.Arguments); err == nil {
					result = res
				} else {
					invErr = err
					result = "工具调用失败: " + err.Error()
					toolSpan.SetStatus(codes.Error, err.Error())
				}
				toolSpan.End()
				if hooks != nil && hooks.OnToolEnd != nil {
					hooks.OnToolEnd(tc.Function.Name, invErr)
				}
				if onChunk != nil {
					onChunk(provider.Chunk{Reasoning: "结果: " + truncate(result, 500) + "\n"})
				}
			}
			conv = append(conv, provider.Message{Role: "tool", Content: result, ToolCallID: tc.ID})
		}
	}
	// 达 MaxSteps 上限：无终稿轮（最后轮仍在调工具），返回空由调用方分流。
	return "", nil
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
	return r.ServeSSEConv(w, ctx, agentID, "", msgs)
}

// ServeSSEConv 带会话 ID 的 SSE 运行（conversationId 空 = 无状态，现状不变）。
func (r *Runtime) ServeSSEConv(w http.ResponseWriter, ctx context.Context, agentID, conversationID string, msgs []provider.Message) error {
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
	err := r.RunConv(ctx, agentID, conversationID, msgs, func(c provider.Chunk) {
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
	if err != nil {
		message := "智能体运行失败，请检查模型通道与执行日志"
		var dependency *dependencyFailure
		if errors.As(err, &dependency) {
			message = dependency.Error()
		}
		writeSSE(map[string]any{"error": message})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	return err
}
