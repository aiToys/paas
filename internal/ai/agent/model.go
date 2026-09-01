// Package agent 实现 AI Agent（P3）：命名预设，组装 system prompt + 工具描述 + RAG 上下文，
// 调底层 LLM（MaaS Provider）。后续切片加 FunctionCalling 多轮 tool 调用循环。
//
// Agent 作为虚拟模型暴露 model:"agent:{id}"（P3.x gateway 路由），开发者像调普通模型一样调 Agent。
//
// 租户私有；不绑物理环境（无 prod:write）。
package agent

import (
	"errors"
	"time"
)

// Agent 命名预设（租户私有）。
type Agent struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenantId"`
	Name           string    `json:"name"` // 租户内唯一
	Description    string    `json:"description"`
	Model          string    `json:"model"`                   // 底层 LLM（如 glm-5.2，走 MaaS catalog）
	SystemPrompt   string    `json:"systemPrompt"`            // 系统提示（与 PromptRef 二选一，前者优先）
	PromptRef      string    `json:"promptRef"`               // 引用 prompt 模板 name（SystemPrompt 为空时用）
	Tools          []string  `json:"tools"`                   // 引用 tool ID 列表（描述注入 system prompt）
	KnowledgeBases []string  `json:"knowledgeBases"`          // 引用 KB ID（RAG 检索注入上下文）
	Skills         []string  `json:"skills"`                  // 引用 skill ID 列表（指令能力包，注入 system prompt）
	Category       string    `json:"category,omitempty"`      // 广场分类
	InstalledFrom  string    `json:"installedFrom,omitempty"` // 来源 marketplace item ID（空=自建）
	MaxSteps       int       `json:"maxSteps"`                // FunctionCalling 最大步数（防死循环，默认 5）
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

const DefaultMaxSteps = 5

// Validate 校验。
func (a Agent) Validate() error {
	if a.Name == "" {
		return fieldErr("name 不能为空")
	}
	if a.Model == "" {
		return fieldErr("model 不能为空（指定底层 LLM）")
	}
	if a.MaxSteps < 0 {
		return fieldErr("maxSteps 不能为负")
	}
	return nil
}

// VirtualModelID 暴露为虚拟模型 ID（gateway 路由 agent:{id} 调用）。
func (a Agent) VirtualModelID() string { return "agent:" + a.ID }

var (
	ErrAgentNotFound = errors.New("agent 不存在")
	ErrAgentExists   = errors.New("agent 已存在")
)

type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func isFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
