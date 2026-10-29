// 官方 Agent 目录（spec §5）：服务端静态注册表，不是租户可编辑的普通 Agent 记录。
// /api/agents 与 /ai 保持现有语义；普通 Agent 的 CRUD/Prompt/Skill 不能修改官方诊断权限。
package assistant

import "errors"

// AgentDesc 官方 Agent 目录项（服务端下发，前端渲染选择器）。
type AgentDesc struct {
	Key         string `json:"key"` // 如 builtin:runtime-diagnosis
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Executor    string `json:"-"`          // 执行器标识（服务端内部，不下发）
	OutputKind  string `json:"outputKind"` // diagnosis_report
	TargetType  string `json:"targetType"` // workload
	// InputKinds 接受的输入形态 / SupportedContextKinds 可解析的上下文锚点
	//（spec 2026-10-29 §9：第二 Agent 注册时声明，复用全部解析与候选基础设施）。
	InputKinds            []string `json:"inputKinds"`
	SupportedContextKinds []string `json:"supportedContextKinds"`
	ReadOnly              bool     `json:"readOnly"`
	// DisabledReason 非空=当前不可用及原因（模型未配置/执行器未装配）；
	// 空=可用。控制台其他功能不因 Agent 不可用受影响。
	DisabledReason string `json:"disabledReason,omitempty"`
}

// BuiltinAgentKeys 官方 Agent key（首期仅智能诊断）。
const (
	AgentKeyRuntimeDiagnosis = "builtin:runtime-diagnosis"
)

// registry 静态目录。DisabledReason 由装配层按运行环境补写（见 cmd/core）。
var registry = []AgentDesc{
	{
		Key:                   AgentKeyRuntimeDiagnosis,
		Name:                  "智能诊断",
		Description:           "对选定的工作负载做只读根因分析：采集运行状态、事件、日志与指标证据，返回可核对的根因假设与建议。不执行任何写操作。",
		Version:               "1",
		Executor:              "diagnose",
		OutputKind:            "diagnosis_report",
		TargetType:            TargetTypeWorkload,
		InputKinds:            []string{"natural_language", "workload", "alert", "trace"},
		SupportedContextKinds: []string{"workload", "alert", "trace"},
		ReadOnly:              true,
	},
}

// ListAgents 目录快照（只读切片拷贝，调用方可安全改写 DisabledReason）。
func ListAgents() []AgentDesc {
	out := make([]AgentDesc, len(registry))
	copy(out, registry)
	return out
}

// LookupAgent 按 key 查目录项。
func LookupAgent(key string) (AgentDesc, bool) {
	for _, a := range registry {
		if a.Key == key {
			return a, true
		}
	}
	return AgentDesc{}, false
}

// ErrUnavailable 表示运行依赖尚未就绪。
var ErrUnavailable = errors.New("agent unavailable")
