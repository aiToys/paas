package gateway

import (
	"errors"
	"net/http"

	"github.com/aitoys/paas/pkg/provider"
)

// Agent 虚拟模型路由错误 sentinels（gateway 包内定义，避免 gateway import agent 破解耦）。
// adapter（cmd/core）把 agent/guardrail 错误映射到这些 sentinel，gateway 据此返 HTTP 状态。
var (
	ErrAgentNotFound = errors.New("agent not found")
	ErrAgentBlocked  = errors.New("agent 内容被护栏拦截")
)

// AgentDispatcher 把 agent:{id} 虚拟模型路由到 Agent runtime（P3）。
//
// Agent 不进 MaaS catalog（catalog 是平台级共享，Agent 是租户私有），
// 而是作为一个虚拟模型 ID（agent:{agentID}）暴露在标准 /v1/chat/completions 下：
// 开发者在 Playground 或 SDK 选 model="agent:<id>" 即可像调普通模型一样调 Agent，
// runtime 内部组装 system prompt（PromptRef/工具描述/KB RAG）后调底层 LLM。
//
// 定义在 gateway 包（而非 import agent）以避免循环依赖：
// agent runtime 依赖 maas/provider，gateway 只依赖此接口，cmd/core 注入实现。
type AgentDispatcher interface {
	// Match 判定 model 是否为 agent 虚拟模型（前缀 "agent:"）。
	Match(model string) bool
	// ServeSSE 以 OpenAI 兼容 SSE 执行 Agent 并写入响应。
	// model 形如 "agent:<id>"，由实现剥前缀解析 agentID。
	ServeSSE(w http.ResponseWriter, r *http.Request, model string, msgs []provider.Message) error
}

// AgentDispatcherHolder 是 AgentDispatcher 的 late-binding 持有者。
//
// 装配顺序问题：mux.Handle("/v1/chat/completions") 注册在 main.go 早期（line ~362），
// 而 agentRuntime 在 KB/凭证等依赖就绪后（line ~758）才构造。
// holder 先以 nil 注册进 ChatCompletions，agentRuntime 构造后 Set 注入；
// 请求时（远晚于启动）holder.v 必已就绪。holder 方法对 nil 内部实现安全降级（Match 返 false）。
type AgentDispatcherHolder struct{ v AgentDispatcher }

// Set 注入真实 dispatcher（agentRuntime 构造后调用）。
func (h *AgentDispatcherHolder) Set(d AgentDispatcher) { h.v = d }

// Match 内部 dispatcher 未注入时返 false（普通模型走 catalog 通道）。
func (h *AgentDispatcherHolder) Match(model string) bool {
	if h == nil || h.v == nil {
		return false
	}
	return h.v.Match(model)
}

// ServeSSE 委托内部 dispatcher；调用方需先 Match 判定。
func (h *AgentDispatcherHolder) ServeSSE(w http.ResponseWriter, r *http.Request, model string, msgs []provider.Message) error {
	return h.v.ServeSSE(w, r, model, msgs)
}
