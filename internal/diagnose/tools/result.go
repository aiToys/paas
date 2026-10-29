// Package tools 实现平台诊断 agent 的 8 个内置只读工具（spec
// tmp/diagnosis-agent/impl-spec.md S3）。
//
// 每个工具 = 固定 JSON Schema + 进程内 invoker（调 controller/observability real
// 的 raw 入口）+ 统一四态返回信封。四态语义（§8坑3 的落地点）：
//
//	ok      查询成功且有数据
//	empty   查询成功但无数据（events TTL 过期 / 时间窗内无日志 / 指标无序列）
//	missing 目标不存在或数据源不覆盖（deployment 不存在 / Loki 不覆盖该 ns）
//	error   上游不可达或查询失败（apiserver 拒绝 / PromQL 语法错）
//
// agent 不靠 LLM 区分这些态——工具实现确定性地分流，LLM 只消费信封。
package tools

import "encoding/json"

// 四态常量。
const (
	StatusOK      = "ok"
	StatusEmpty   = "empty"
	StatusMissing = "missing"
	StatusError   = "error"
)

// ToolResult 统一四态返回信封（invoker 序列化为 JSON 喂 LLM）。
type ToolResult struct {
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
	Note   string `json:"note,omitempty"`
}

// JSON 序列化信封（错误时返 error 态信封，绝不返裸错误文本——LLM 需要统一结构）。
func (r ToolResult) JSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		// Data 含不可序列化对象时的兜底（理论不可达，防 panic 卡死循环）。
		return `{"status":"error","note":"工具结果序列化失败"}`
	}
	return string(b)
}

// 四态便捷构造器。
func okResult(data any, note string) ToolResult {
	return ToolResult{Status: StatusOK, Data: data, Note: note}
}
func emptyResult(data any, note string) ToolResult {
	return ToolResult{Status: StatusEmpty, Data: data, Note: note}
}
func missingResult(note string) ToolResult {
	return ToolResult{Status: StatusMissing, Note: note}
}
func errorResult(note string) ToolResult {
	return ToolResult{Status: StatusError, Note: note}
}
