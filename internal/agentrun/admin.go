// admin 总览（super_admin 只读）：上游 ns=agent 单一命名空间（契约 v1，租户维度上游未做），
// 故总览无租户列——run 归因按名称前缀粗分（auto-review-*/auto-test-* 为 agentrun 侧 D8 webhook
// 自动触发，其余为 paas 委派/agent stage 触发），成本大头（auto-*）与业务触发分开可见。
package agentrun

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// AdminRun admin 总览视图（GET /api/runs 列表项的展示子集；request/summary 服务端截断防响应膨胀）。
type AdminRun struct {
	Name           string `json:"name"`
	Runtime        string `json:"runtime"`
	Stage          string `json:"stage"`
	Phase          string `json:"phase"` // Pending|Running|Succeeded|Failed
	Verdict        string `json:"verdict,omitempty"`
	Tokens         int64  `json:"tokens"`
	PodName        string `json:"podName,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"` // 上游格式 "2026-10-12 15:09:18"，透传前端展示
	Age            string `json:"age,omitempty"`
	Request        string `json:"request,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Error          string `json:"error,omitempty"`
	Source         string `json:"source"` // auto-review|auto-test|paas
	WorkspaceFrom  string `json:"workspaceFrom,omitempty"`
	ProducedBranch string `json:"producedBranch,omitempty"`
}

// AdminStats 汇总计数（phase 分布 + token 按来源合计）。
type AdminStats struct {
	Total     int64 `json:"total"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Running   int64 `json:"running"`
	Pending   int64 `json:"pending"`
	// Unknown 未知 phase 独立计数（上游新增状态时不误并入 Pending——pending=排队等待，
	// 把 Cancelled 之类未知态塞进它会虚高涨班队列，R3 审计 L-2）。
	Unknown     int64 `json:"unknown"`
	TokensTotal int64 `json:"tokensTotal"`
	TokensAuto  int64 `json:"tokensAuto"` // agentrun 侧 webhook 自动 run 合计（无人为控制，成本可见性）
	TokensPaas  int64 `json:"tokensPaas"` // paas 委派/agent stage 触发合计
}

// AdminOverview 总览响应：runs 明细（新→旧）+ stats 汇总。
type AdminOverview struct {
	Runs  []AdminRun `json:"runs"`
	Stats AdminStats `json:"stats"`
}

// maxTextLen request/summary 截断长度（run 数量增长后列表响应防膨胀；详情看 /api/agentrun/runs/{name}）。
const maxTextLen = 200

// truncate UTF-8 安全截断（中文需求文本不切半字符）。
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// paasRunNameRe paas 编排 agent stage 的 run 名形态：<runID>-<stageIdx>（retry 再加 -rN），
// runID 固定 `run-<12hex>`（store newID("run") 生成）。可证实才归 paas——
// default 归 paas 会把用户在 agentrun 侧手动触发的 run 误算进 paas token 归因（R3 审计 M-4）。
var paasRunNameRe = regexp.MustCompile(`^run-[0-9a-f]{12}-\d+(-r\d+)?$`)

// runSource 按名称归类来源：可证实前缀归类，无法证实归 "other" 独立桶（token 合计不受影响）。
func runSource(name string) string {
	switch {
	case strings.HasPrefix(name, "auto-review-"):
		return "auto-review"
	case strings.HasPrefix(name, "auto-test-"):
		return "auto-test"
	case paasRunNameRe.MatchString(name):
		return "paas"
	default:
		return "other"
	}
}

// ListRuns 拉上游全量 run（平台 token 通道；上游返回裸数组，新→旧）。
func (c *Client) ListRuns(ctx context.Context) ([]AdminRun, error) {
	var raw []AdminRun
	if err := c.do(ctx, http.MethodGet, "/api/runs", nil, &raw); err != nil {
		return nil, err
	}
	for i := range raw {
		raw[i].Request = truncate(raw[i].Request, maxTextLen)
		raw[i].Summary = truncate(raw[i].Summary, maxTextLen)
		raw[i].Error = truncate(raw[i].Error, maxTextLen)
		raw[i].Source = runSource(raw[i].Name)
	}
	return raw, nil
}

// Overview ListRuns + 汇总统计（admin 页统计卡片数据源）。
func (c *Client) Overview(ctx context.Context) (AdminOverview, error) {
	runs, err := c.ListRuns(ctx)
	if err != nil {
		return AdminOverview{}, err
	}
	ov := AdminOverview{Runs: runs}
	for _, r := range runs {
		ov.Stats.Total++
		ov.Stats.TokensTotal += r.Tokens
		switch r.Source {
		case "paas":
			ov.Stats.TokensPaas += r.Tokens
		default:
			// auto-review/auto-test/other 都计入自动侧合计（other=来源不可证实，
			// 不冒充 paas 归因；总量 TokensTotal 兜底）。
			ov.Stats.TokensAuto += r.Tokens
		}
		switch r.Phase {
		case "Succeeded", "succeeded":
			ov.Stats.Succeeded++
		case "Failed", "failed":
			ov.Stats.Failed++
		case "Running", "running":
			ov.Stats.Running++
		case "Pending", "pending":
			ov.Stats.Pending++
		default:
			ov.Stats.Unknown++
		}
	}
	return ov, nil
}
