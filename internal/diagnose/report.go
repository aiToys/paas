// Package diagnose 平台故障诊断 agent（只读根因诊断，spec
// tmp/diagnosis-agent/impl-spec.md S4/S5）：结构化输入输出契约 + 输出校验层。
//
// 诊断 agent = runtime.RunDiagnose 多轮工具循环 + 8 个内置只读工具（tools 包）+
// 本包的 system prompt 约束与输出 schema 校验。输出报告即 v2 修复闭环的输入
// （agentrun.CreateRunAsUser 接口已就位），保证 schema 100% 可解析是硬要求。
package diagnose

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Request POST /api/admin/diagnose/run 输入（handoff §3 契约）。
type Request struct {
	Trigger    string      `json:"trigger"` // manual | event
	Target     DiagTarget  `json:"target"`
	TimeWindow DiagWindow  `json:"timeWindow"`
	Options    DiagOptions `json:"options"`
}

// DiagTarget 诊断目标（symptom 见 SKILL 理念：Pod 异常/延迟/错误率/未知）。
type DiagTarget struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Pod       string `json:"pod,omitempty"`
	Service   string `json:"service,omitempty"` // Jaeger service.name（默认同 workload）
	Symptom   string `json:"symptom,omitempty"` // CrashLoop|延迟|错误率|未知
}

// DiagWindow 时间窗。
type DiagWindow struct {
	LookbackMinutes int `json:"lookbackMinutes"` // 默认 30
}

// DiagOptions 运行选项。
type DiagOptions struct {
	MaxTokens int    `json:"maxTokens,omitempty"` // 留位（透传 LLM），v1 未启用
	Depth     string `json:"depth,omitempty"`     // quick|full（quick 少查几跳），v1 全量
}

// Normalize 填默认值 + 基本校验。
func (r *Request) Normalize() error {
	if r.Target.Namespace == "" {
		return fmt.Errorf("target.namespace 必填")
	}
	if r.Target.Workload == "" && r.Target.Pod == "" && r.Target.Service == "" {
		return fmt.Errorf("target 需至少指定 workload/pod/service 之一")
	}
	if r.Trigger == "" {
		r.Trigger = "manual"
	}
	if r.TimeWindow.LookbackMinutes <= 0 {
		r.TimeWindow.LookbackMinutes = 30
	}
	if r.Target.Service == "" {
		r.Target.Service = r.Target.Workload
	}
	return nil
}

// Report 结构化诊断报告（handoff §3 输出契约；v2 修复闭环的输入）。
type Report struct {
	Verdict             Verdict               `json:"verdict"`
	RootCauseHypotheses []RootCauseHypothesis `json:"rootCauseHypotheses"`
	RecommendedActions  []RecommendedAction   `json:"recommendedActions"`
	DataSourceCoverage  map[string]string     `json:"dataSourceCoverage"`
	ConfidenceNote      string                `json:"confidenceNote"`
	RawOutput           string                `json:"rawOutput,omitempty"` // LLM 原始文本（校验失败降级时保留证据）
	TokensUsed          int                   `json:"tokensUsed"`
	DurationSeconds     float64               `json:"durationSeconds"`
}

type Verdict struct {
	Severity string `json:"severity"` // critical | warning | info
	Category string `json:"category"` // code_bug | config_error | resource_exhaustion | dependency_failure | infra | unknown
	Summary  string `json:"summary"`
}

type RootCauseHypothesis struct {
	Hypothesis string     `json:"hypothesis"`
	Confidence float64    `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
}

type Evidence struct {
	Source string `json:"source"` // 工具名或数据源（get_pod_logs/prometheus/...）
	Detail string `json:"detail"`
	Weight string `json:"weight"` // high | medium | low
}

type RecommendedAction struct {
	Action           string `json:"action"`
	Type             string `json:"type"` // config | code
	Risk             string `json:"risk"` // low | medium | high（诊断只读，动作由人执行）
	RequiresApproval bool   `json:"requiresApproval"`
}

// extractJSON 从 LLM 混排输出中提取报告 JSON 文本：围栏优先（多个 ```json 块取
// 最后一个=终稿）；无围栏按 "verdict" 键定位、向前回溯最近 '{' 作起点、取其后
// 最后一个 '}' 作终点（兼容紧凑 {"verdict" 与 pretty "{\n  \"verdict\" 两种形态；
// 真实集群两跑分别踩中「中文正文+围栏」与「中文正文+裸 pretty JSON」形态）。
func ExtractJSON(out string) string {
	if i := strings.LastIndex(out, "```json"); i >= 0 {
		body := out[i+len("```json"):]
		if j := strings.Index(body, "```"); j >= 0 {
			body = body[:j]
		}
		return strings.TrimSpace(body)
	}
	if i := strings.Index(out, `"verdict"`); i > 0 {
		start := strings.LastIndex(out[:i], "{")
		end := strings.LastIndex(out, "}")
		if start >= 0 && end > start {
			return out[start : end+1]
		}
	}
	return strings.TrimSpace(out)
}

// ParseReport 解析并校验 LLM 最终输出（S4.3 输出校验层）。
//   - 不可解析 JSON：返降级报告（severity=info、confidenceNote 说明失败、RawOutput 留证），
//     不假装成功——诊断价值在证据链，格式坏时宁可明说。
//   - 每条假设必须 ≥1 证据：缺证据的假设剔除并把违规写进 ConfidenceNote（降级透明）。
//   - 数据源缺失不编造：dataSourceCoverage 的 missing/empty 原样保留。
func ParseReport(llmOutput string) Report {
	degraded := func(reason string) Report {
		return Report{
			Verdict:             Verdict{Severity: "info", Category: "unknown", Summary: "诊断报告格式校验失败，原始输出见 rawOutput"},
			RootCauseHypotheses: []RootCauseHypothesis{},
			RecommendedActions:  []RecommendedAction{},
			DataSourceCoverage:  map[string]string{},
			ConfidenceNote:      reason,
			RawOutput:           llmOutput,
		}
	}
	// LLM 输出形态不定：可能纯 JSON、```json 围栏包裹、或「思考正文 + 围栏 JSON」混排
	//（真实集群首跑即踩中第三种：deepseek 输出长段中文推理后落围栏报告）。
	// 提取策略：优先取 ```json 围栏内容（多个时取最后一个=终稿）；无围栏则用
	// {"verdict" 锚点截首到最后一个 '}'；都失败降级。
	text := ExtractJSON(llmOutput)
	var rep Report
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		return degraded("LLM 输出不可解析为 JSON 报告（" + err.Error() + "），结论不可信，请查看 rawOutput")
	}
	// 假设 ≥1 证据（§7 成功标准）。
	var kept []RootCauseHypothesis
	dropped := 0
	for _, h := range rep.RootCauseHypotheses {
		if len(h.Evidence) > 0 {
			kept = append(kept, h)
		} else {
			dropped++
		}
	}
	rep.RootCauseHypotheses = kept
	if dropped > 0 {
		note := fmt.Sprintf("校验剔除 %d 条无证据假设（每条假设必须 ≥1 证据）", dropped)
		if rep.ConfidenceNote != "" {
			rep.ConfidenceNote = note + "；" + rep.ConfidenceNote
		} else {
			rep.ConfidenceNote = note
		}
	}
	if rep.DataSourceCoverage == nil {
		rep.DataSourceCoverage = map[string]string{}
	}
	return rep
}

// SystemPrompt 诊断 agent 的系统提示（吸收 skills/onepaas-runtime-diagnose SKILL.md
// 理念 + handoff §5 执行流程 + §8 四坑的 LLM 侧约束；工具侧已确定性处理四坑，
// prompt 是第二道防线）。输出必须是纯 JSON（无围栏无解释文本）。
func SystemPrompt() string {
	return `你是 OnePaaS 平台的根因诊断 agent（只读）。对给定的目标（namespace/workload/pod/服务），
用 8 个只读工具采集证据，输出带证据链的结构化根因诊断报告。你绝不能执行任何写操作。

## 执行流程
1. 规范化目标：workload → 经 get_workload_status 找到 pods（selector 由工具从 deployment spec 反查，不要猜 label）。
2. 第一跳并行信号：get_workload_status + get_k8s_events（warningOnly=true）+ get_pod_metrics。
3. 初步判断 category（code_bug | config_error | resource_exhaustion | dependency_failure | infra | unknown）。
4. 按 category 定向深挖：
   - 资源类 → query_prometheus（趋势）+ get_workload_spec（limit 真源）
   - 崩溃类 → get_pod_logs（previous=true 看崩溃现场）+ query_loki（error 关键字）
   - 依赖慢 → query_jaeger_traces（慢 span）+ query_loki
   - 配置类 → get_workload_spec（资源/env/镜像）
5. 证据加权汇总：每条假设标注 confidence（0-1）并列出全部证据（source=工具名，weight=high/medium/low）。
6. 输出最终报告（纯 JSON，见下方 schema）。

## 硬性纪律
- 每条假设必须 ≥1 条证据；没有证据的假设不要写。
- 工具返回 status=missing（目标不存在/数据源不覆盖）或 empty（查到没数据）时，如实记入
  dataSourceCoverage，绝不把「没查到」当成「没问题」，也绝不编造数据。
- Pod Running 不等于 Ready；Ready 数低于期望副本时必须深挖容器级 reason。
- get_pod_metrics 的 limit 只信 specLimits（deployment spec）；Prometheus 的 limit 指标不可信。
- query_jaeger_traces 若 note 标注「历史 trace」，这些 trace 不能作为当前状态证据。
- 事件窗口内无事件是正常现象（TTL），不是「无故障」的证据。
- recommendedActions 只描述操作（描述给平台管理员执行），你自己绝不执行；requiresApproval 默认 true。

## 输出 schema（最终回复必须是可被 json.Unmarshal 解析的纯 JSON，不要围栏不要多余文本）
{
  "verdict": {"severity":"critical|warning|info","category":"code_bug|config_error|resource_exhaustion|dependency_failure|infra|unknown","summary":"一句话结论"},
  "rootCauseHypotheses": [{"hypothesis":"...","confidence":0.0-1.0,"evidence":[{"source":"工具名","detail":"...","weight":"high|medium|low"}]}],
  "recommendedActions": [{"action":"...","type":"config|code","risk":"low|medium|high","requiresApproval":true}],
  "dataSourceCoverage": {"prometheus":"ok|empty|missing|error","loki":"...","jaeger":"...","events":"...","k8s":"ok|empty|missing|error"},
  "confidenceNote": "对结论可信度的诚实说明（哪些数据源缺失导致哪些分支无法排除）",
  "tokensUsed": 0,
  "durationSeconds": 0
}
tokensUsed 与 durationSeconds 由平台回填，你填 0。`
}

// UserPrompt 组装诊断请求的用户消息。
func UserPrompt(req Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "诊断目标：namespace=%s", req.Target.Namespace)
	if req.Target.Workload != "" {
		fmt.Fprintf(&b, " workload=%s", req.Target.Workload)
	}
	if req.Target.Pod != "" {
		fmt.Fprintf(&b, " pod=%s", req.Target.Pod)
	}
	if req.Target.Service != "" {
		fmt.Fprintf(&b, " service=%s", req.Target.Service)
	}
	if req.Target.Symptom != "" {
		fmt.Fprintf(&b, "\n症状：%s", req.Target.Symptom)
	}
	fmt.Fprintf(&b, "\n时间窗：近 %d 分钟", req.TimeWindow.LookbackMinutes)
	fmt.Fprintf(&b, "\n触发方式：%s", req.Trigger)
	b.WriteString("\n请按执行流程采集证据并输出 JSON 诊断报告。")
	return b.String()
}

// DefaultModel 主控模型默认值（env PAAS_DIAGNOSE_MODEL 覆盖；impl-spec 决策 2：
// 不写死进 agent 预设，装配层读 env）。价格/能力见 internal/maas/airouter_catalog.go。
const DefaultModel = "deepseek-v4.1-flash"

// MaxSteps 诊断工具循环上限（一跳 3 工具 + 定向深挖 2-4 工具 + 复查，12 步足够且防失控）。
const MaxSteps = 12

// Timeout 单次诊断整体超时（同步 REST；LLM + 工具循环 P95 目标 <90s，留余量）。
const Timeout = 3 * time.Minute
