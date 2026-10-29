// 证据账本（spec §7.1）：每次工具执行由后端生成 evidenceId、四态 + reasonCode、
// 脱敏摘要、有界片段与内容 hash。原始采集内容先过滤、脱敏、截断，再进入模型与存储；
// LLM 只返回假设与 evidenceIds，报告校验器拒绝引用不存在或其他 run 的 ID。
package scoped

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// 四态 + reasonCode（spec §7：覆盖状态由工具实际执行结果汇总，不能由 LLM 填写）。
const (
	StatusOK      = "ok"
	StatusEmpty   = "empty"
	StatusMissing = "missing"
	StatusError   = "error"
)

// 常用 reasonCode。
const (
	ReasonNotConfigured     = "not_configured"     // 数据源未接入（env 未配）
	ReasonNotChecked        = "not_checked"        // 本次未执行（区别于确认未采集）
	ReasonScopeUnverifiable = "scope_unverifiable" // 归属不可证明（如链路重名服务）
	ReasonNotFound          = "not_found"          // 授权对象不存在（deployment 未拉起等）
	ReasonTruncated         = "truncated"          // 片段超限截断
)

// 预算上限（spec §8：单工具 64 KiB 脱敏数据、单任务证据总量 1 MiB）。
const (
	MaxSnippetBytes = 64 * 1024
	MaxTotalBytes   = 1024 * 1024
)

// Evidence 用户侧证据（账本投影；进入模型与存储前已过滤/脱敏/截断）。
type Evidence struct {
	ID          string    `json:"id"` // ev-<n>（当前 run 内唯一）
	Tool        string    `json:"tool"`
	Status      string    `json:"status"` // ok/empty/missing/error
	ReasonCode  string    `json:"reasonCode,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
	WindowMin   int       `json:"windowMin,omitempty"`
	Summary     string    `json:"summary"`
	Snippet     string    `json:"snippet,omitempty"` // 有界脱敏片段
	ContentHash string    `json:"contentHash,omitempty"`
}

// Ledger 当前 run 的证据账本（非并发安全——单 run 单 worker 顺序执行工具）。
type Ledger struct {
	entries    []Evidence
	totalBytes int
	truncated  bool
}

// NewLedger 空账本。
func NewLedger() *Ledger { return &Ledger{} }

var (
	// 敏感模式（进模型与存储前脱敏；命中替换为 ***）。
	reBearer   = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._\-]{8,}`)
	reAPIKey   = regexp.MustCompile(`sk-[a-z0-9]{16,}`)
	rePassword = regexp.MustCompile(`(?i)(password|passwd|pwd)=([^\s&"']+)`)
	reConnStr  = regexp.MustCompile(`(?i)(postgres|mysql|redis|mongodb|amqp)://[^\s"']+@[^\s"']+`)
	reTokenKV  = regexp.MustCompile(`(?i)(token|secret|api[_-]?key|password|passwd|pwd)["']?\s*[:=]\s*["']?([^\s,"'}]+)`)
)

// MaskSensitive 脱敏（日志/事件文本进模型与存储前统一过这里）。
func MaskSensitive(s string) string {
	s = reConnStr.ReplaceAllString(s, "$1://***:***@***")
	s = reBearer.ReplaceAllString(s, "bearer ***")
	s = reAPIKey.ReplaceAllString(s, "sk-***")
	s = rePassword.ReplaceAllString(s, "$1=***")
	s = reTokenKV.ReplaceAllString(s, `$1=***`)
	return s
}

// Record 记一条证据并返回 evidenceId（ev-<seq>）。summary/snippet 落账前脱敏；
// snippet 超单条上限截断并标 truncated；总量超 1 MiB 后仅记摘要不再存片段。
func (l *Ledger) Record(tool, status, reasonCode, summary, snippet string, windowMin int) string {
	id := fmt.Sprintf("ev-%d", len(l.entries)+1)
	summary = MaskSensitive(truncateRunes(summary, 500))
	snippet = MaskSensitive(snippet)
	rc := reasonCode
	if len(snippet) > MaxSnippetBytes {
		snippet = truncateRunes(snippet, MaxSnippetBytes/3) // rune 截断防切多字节
		if rc == "" {
			rc = ReasonTruncated
		}
	}
	if l.totalBytes+len(snippet) > MaxTotalBytes {
		snippet = "" // 总量超限：保留四态与摘要，片段丢弃并标记
		rc = ReasonTruncated
		l.truncated = true
	}
	l.totalBytes += len(snippet)
	l.entries = append(l.entries, Evidence{
		ID: id, Tool: tool, Status: status, ReasonCode: rc,
		ObservedAt: time.Now().UTC(), WindowMin: windowMin,
		Summary: summary, Snippet: snippet,
		ContentHash: hashSnippet(snippet),
	})
	return id
}

// Snapshot 证据视图（外部只读副本）。
func (l *Ledger) Snapshot() []Evidence {
	out := make([]Evidence, len(l.entries))
	copy(out, l.entries)
	return out
}

// Coverage 数据源覆盖汇总（源摘要不覆盖子查询错误；traces 首期 scope_unverifiable）。
// 未执行的源记 missing/not_checked（spec §7：与确认未采集区分）。
func (l *Ledger) Coverage() map[string]string {
	cover := map[string]string{"k8s": "missing/not_checked", "events": "missing/not_checked", "loki": "missing/not_checked", "prometheus": "missing/not_checked", "releases": "missing/not_checked"}
	sourceOf := map[string]string{ // 工具 → 源
		"get_workload_status": "k8s", "get_workload_spec": "k8s",
		"get_k8s_events": "events", "get_pod_logs": "k8s",
		"query_loki": "loki", "get_pod_metrics": "prometheus",
		"get_recent_release": "releases",
	}
	for _, e := range l.entries {
		src := sourceOf[e.Tool]
		if src == "" {
			continue
		}
		// 源摘要：ok 优先，其次已有 error 保留，empty 不覆盖 ok/error。
		if cur, seen := cover[src]; !seen || cur == "missing/not_checked" || (cur != StatusError && (e.Status == StatusError || cur == StatusEmpty)) {
			cover[src] = e.Status
		}
	}
	// 链路首期降级（spec §6.3：service.name 白名单不能证明跨租户重名归属）。
	cover["traces"] = StatusMissing + "/" + ReasonScopeUnverifiable
	return cover
}

// Truncated 总量截断标记（报告限制说明用）。
func (l *Ledger) Truncated() bool { return l.truncated }

func hashSnippet(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…(截断)"
}

// RedactForModel 工具结果进模型前的最终收敛（账本已脱敏；此处保证有界）。
func RedactForModel(s string) string {
	return MaskSensitive(truncateRunes(s, MaxSnippetBytes/3))
}
