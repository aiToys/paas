// 上下文解析器（spec 2026-10-29 §4-§5）：ContextResolver 是唯一把用户线索变成
// DiagnosticContext 的入口；最终授权仍收敛到 scoped.TargetResolver 五步解析。
// 解析优先级：结构化锚点 > 文本明确 ID（Trace/工作负载/告警）> 受控名称匹配。
// 解析失败不得默认扫描租户，也不得按模糊名称猜测环境。
package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/diagnose/scoped"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// timeWindow 诊断观察窗（锚点解析时对齐事件时间；文本解析缺省 now-30m）。
type timeWindow struct {
	start, end time.Time
}

func (w timeWindow) isZero() bool { return w.start.IsZero() }

// WorkloadLister 本租户 service 型工作负载列表（名称匹配候选用；cmd/core 桥接）。
type WorkloadLister interface {
	ListWorkloads(ctx context.Context) ([]scoped.WorkloadInfo, error)
}

// AlertEventFinder 告警事件按 ID 精查（PG 路径；内存模式告警不持久化→不装配）。
type AlertEventFinder interface {
	GetAlertEvent(ctx context.Context, id string) (observability.AlertEvent, error)
}

// TraceFinder 受控 Trace 读取（复用 observability GetTrace：租户归属已 fail-closed）。
type TraceFinder interface {
	GetTrace(ctx context.Context, traceID string) (observability.Trace, error)
}

// ContextResolver 上下文解析（依赖全部窄接口，cmd/core 装配层注入）。
type ContextResolver struct {
	Targets   *scoped.TargetResolver
	Workloads WorkloadLister
	Alerts    AlertEventFinder
	Traces    TraceFinder
}

// resolutionOutcome 一次解析的结果（状态编码在 context.ResolutionStatus）。
// need_choice 时 candidates 非空；其余状态 candidates 为空。
type resolutionOutcome struct {
	context    DiagnosticContext
	candidates []ContextCandidate
}

// 文本线索提取（确定性正则，不依赖模型；模型提取候选留后续，且必须经同一校验链）。
var (
	traceIDPattern    = regexp.MustCompile(`\b([0-9a-f]{16}|[0-9a-f]{32})\b`)
	workloadIDPattern = regexp.MustCompile(`\b(wl-[a-z0-9][a-z0-9-]{0,63})\b`)
	alertIDPattern    = regexp.MustCompile(`\b(rule-[0-9a-f]{6,16})\b`)
)

// 时间窗策略：告警/Trace 锚点围绕发生时间前后各留观察余量（诊断要看诱因与恢复）。
const (
	anchorWindowPad = 15 * time.Minute
	defaultLookback = 30 * time.Minute
)

// Resolve 结构化锚点解析（页面深链：workload/alert/trace 详情页带入）。
func (r *ContextResolver) Resolve(ctx context.Context, anchor ContextInput) (resolutionOutcome, error) {
	anchor.Kind = strings.ToLower(strings.TrimSpace(anchor.Kind))
	anchor.ID = strings.TrimSpace(anchor.ID)
	if anchor.ID == "" {
		return resolutionOutcome{}, errors.New("context.id 不能为空")
	}
	switch anchor.Kind {
	case ContextKindWorkload:
		return r.resolveWorkloadAnchor(ctx, DiagnosticAnchor{Kind: ContextKindWorkload, ID: anchor.ID})
	case ContextKindAlert:
		return r.resolveAlert(ctx, DiagnosticAnchor{Kind: ContextKindAlert, ID: anchor.ID})
	case ContextKindTrace:
		return r.resolveTrace(ctx, DiagnosticAnchor{Kind: ContextKindTrace, ID: anchor.ID})
	case ContextKindLog:
		// 日志上下文没有独立可授权实体：引导到工作负载/Trace（不静默扩大范围）。
		return resolutionOutcome{context: DiagnosticContext{
			Anchors:          []DiagnosticAnchor{{Kind: ContextKindLog, ID: anchor.ID}},
			ResolutionStatus: ResolutionNeedClarification, Source: ContextKindLog,
			ResolutionReason: "日志上下文需要关联的工作负载或 Trace 才能限定诊断范围，请提供其一",
		}}, nil
	default:
		return resolutionOutcome{}, errors.New("context.kind 仅支持 workload/alert/trace")
	}
}

// ResolveText 自然语言解析（spec §4 优先级 2/3：明确 ID > 受控名称匹配）。
// 无法定位时 need_clarification（最小追问，不扫描租户）。
func (r *ContextResolver) ResolveText(ctx context.Context, text string) (resolutionOutcome, error) {
	// 1. 文本中的明确 ID（优先级最高，确定性命中）。
	if m := workloadIDPattern.FindString(text); m != "" {
		return r.resolveWorkloadAnchor(ctx, DiagnosticAnchor{Kind: ContextKindWorkload, ID: m})
	}
	if m := traceIDPattern.FindString(text); m != "" {
		return r.resolveTrace(ctx, DiagnosticAnchor{Kind: ContextKindTrace, ID: m})
	}
	if m := alertIDPattern.FindString(text); m != "" {
		return r.resolveAlert(ctx, DiagnosticAnchor{Kind: ContextKindAlert, ID: m})
	}
	// 2. 受控名称匹配：本租户 service 工作负载名/服务名精确匹配（大小写不敏感）。
	out, err := r.matchByName(ctx, text)
	if err != nil || out.context.ResolutionStatus != ResolutionNeedClarification {
		return out, err
	}
	// 3. 无任何线索：最小追问。
	out.context.Source = ContextSourceText
	out.context.ResolutionReason = "请补充要诊断的目标：服务或工作负载名称、工作负载 ID（wl- 开头）、告警 ID（rule- 开头）或 Trace ID"
	return out, nil
}

// resolveWorkloadAnchor 工作负载 ID → 授权目标（scoped 五步解析）。
func (r *ContextResolver) resolveWorkloadAnchor(ctx context.Context, anchor DiagnosticAnchor) (resolutionOutcome, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return resolutionOutcome{}, err
	}
	t, err := r.Targets.Resolve(ctx, tid, anchor.ID)
	if err != nil {
		// 不泄漏跨租户存在性：统一按「无法定位」降级为追问。
		return resolutionOutcome{context: DiagnosticContext{
			Anchors: []DiagnosticAnchor{anchor}, Source: anchor.Kind,
			ResolutionStatus: ResolutionNeedClarification,
			ResolutionReason: "未找到该工作负载（或当前账号无权访问），请确认 ID",
		}}, nil
	}
	now := time.Now().UTC()
	ws, we := ctxWindow(now.Add(-defaultLookback), now)
	return resolutionOutcome{context: DiagnosticContext{
		Anchors: []DiagnosticAnchor{anchor}, Source: anchor.Kind, Resolved: &t,
		WindowStart: ws, WindowEnd: we,
		ResolutionStatus: ResolutionResolved,
	}}, nil
}

// resolveAlert 告警 → 规则目标维度 → workload 直接解析 / app 展开候选 /
// dataservice 如实不支持（spec §5：无法关联时返回原因，不把告警文本交给跨租户工具）。
func (r *ContextResolver) resolveAlert(ctx context.Context, anchor DiagnosticAnchor) (resolutionOutcome, error) {
	if r.Alerts == nil {
		return unavailableOutcome(anchor, ContextKindAlert, "告警历史未启用（需配置 PostgreSQL），无法按告警 ID 解析"), nil
	}
	ev, err := r.Alerts.GetAlertEvent(ctx, anchor.ID)
	if err != nil || ev.ID == "" {
		return unavailableOutcome(anchor, ContextKindAlert, "告警不存在或当前账号无权访问"), nil
	}
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return resolutionOutcome{}, err
	}
	// 租户双保险（finder 按租户过滤，此处再断言；不一致按不存在处理不泄漏）。
	if ev.TenantID != "" && ev.TenantID != tid {
		return unavailableOutcome(anchor, ContextKindAlert, "告警不存在或当前账号无权访问"), nil
	}
	windowStart := ev.FiredAt.Add(-anchorWindowPad)
	windowEnd := latest(ev.OccurredAt.Add(anchorWindowPad), ev.FiredAt.Add(anchorWindowPad))
	switch ev.TargetType {
	case "workload":
		out, _ := r.resolveWorkloadAnchor(ctx, DiagnosticAnchor{Kind: ContextKindWorkload, ID: ev.TargetID})
		if out.context.ResolutionStatus == ResolutionResolved {
			// 保留告警锚点（诊断窗口对齐告警发生时间而非 now）。
			w0 := clampWindow(windowStart, windowEnd)
			out.context.Anchors = []DiagnosticAnchor{anchor, {Kind: ContextKindWorkload, ID: ev.TargetID}}
			out.context.Source = ContextKindAlert
			out.context.WindowStart, out.context.WindowEnd = ctxWindow(w0.start, w0.end)
			return out, nil
		}
		return unavailableOutcome(anchor, ContextKindAlert, "告警关联的工作负载当前不可访问"), nil
	case "app":
		wls, err := r.listServices(ctx)
		if err != nil {
			return unavailableOutcome(anchor, ContextKindAlert, "资源查询失败，请稍后重试"), nil
		}
		matched := filterBy(wls, func(w scoped.WorkloadInfo) bool { return w.AppID == ev.TargetID })
		return r.choiceOrSingle(ctx, anchor, ContextKindAlert, matched, clampWindow(windowStart, windowEnd))
	case "dataservice":
		return unavailableOutcome(anchor, ContextKindAlert, "数据服务告警请从数据服务详情页发起诊断（当前助手面向工作负载）"), nil
	default:
		return unavailableOutcome(anchor, ContextKindAlert, "告警目标类型不支持诊断（"+ev.TargetType+"）"), nil
	}
}

// resolveTrace Trace → 租户归属（GetTrace 内部 fail-closed）→ span service.name
// 匹配本租户工作负载 → 唯一 resolved / 多候选 / 无法关联（spec §5）。
func (r *ContextResolver) resolveTrace(ctx context.Context, anchor DiagnosticAnchor) (resolutionOutcome, error) {
	if r.Traces == nil {
		return unavailableOutcome(anchor, ContextKindTrace, "链路追踪未接入，无法按 Trace ID 解析"), nil
	}
	tr, err := r.Traces.GetTrace(ctx, anchor.ID)
	if err != nil || tr.ID == "" {
		// GetTrace 跨租户/未配置统一 ErrTraceNotFound：归属不可证明即拒绝（不泄漏）。
		return unavailableOutcome(anchor, ContextKindTrace, "Trace 不存在或无法证明归属于当前租户"), nil
	}
	wls, err := r.listServices(ctx)
	if err != nil {
		return unavailableOutcome(anchor, ContextKindTrace, "资源查询失败，请稍后重试"), nil
	}
	// trace 内 distinct service.name（本租户白名单已在 GetTrace 内校验）。
	names := map[string]bool{}
	for _, sp := range tr.Spans {
		if sp.Service != "" {
			names[sp.Service] = true
		}
	}
	matched := filterBy(wls, func(w scoped.WorkloadInfo) bool {
		return names[w.Name] || (w.Service != "" && names[w.Service])
	})
	start := tr.StartedAt
	if start.IsZero() {
		start = time.Now().UTC()
	}
	window := clampWindow(start.Add(-anchorWindowPad), start.Add(time.Duration(tr.DurationMs)*time.Millisecond+anchorWindowPad))
	return r.choiceOrSingle(ctx, anchor, ContextKindTrace, matched, window)
}

// matchByName 受控名称匹配：workload 名或服务名精确等值（不多环境猜测）。
func (r *ContextResolver) matchByName(ctx context.Context, text string) (resolutionOutcome, error) {
	wls, err := r.listServices(ctx)
	if err != nil {
		return resolutionOutcome{context: DiagnosticContext{ResolutionStatus: ResolutionNeedClarification, Source: ContextSourceText,
			ResolutionReason: "资源查询失败，请稍后重试或直接提供工作负载 ID"}}, nil
	}
	// 抽取候选词：整句按空白/标点切分（中文无空格场景靠整句精确匹配兜底）。
	tokens := nameTokens(text)
	matched := filterBy(wls, func(w scoped.WorkloadInfo) bool {
		if w.Name != "" && (tokens[w.Name] || w.Name == strings.TrimSpace(text)) {
			return true
		}
		return w.Service != "" && (tokens[w.Service] || w.Service == strings.TrimSpace(text))
	})
	out, _ := r.choiceOrSingle(ctx, DiagnosticAnchor{}, ContextSourceText, matched, timeWindow{})
	return out, nil
}

// choiceOrSingle 候选收敛：0=追问 / 1=resolved / 多=need_choice（候选服务端生成）。
func (r *ContextResolver) choiceOrSingle(ctx context.Context, anchor DiagnosticAnchor, source string, matched []scoped.WorkloadInfo, window timeWindow) (resolutionOutcome, error) {
	switch {
	case len(matched) == 0:
		return resolutionOutcome{context: DiagnosticContext{
			Anchors: anchorsOf(anchor), Source: source,
			ResolutionStatus: ResolutionNeedClarification,
			ResolutionReason: "没能定位到对应的服务，请补充环境或直接提供工作负载/告警/Trace ID",
		}}, nil
	case len(matched) == 1:
		return r.resolvedFromWorkload(ctx, anchor, source, matched[0], window)
	default:
		cands := make([]ContextCandidate, 0, len(matched))
		for _, w := range matched {
			cands = append(cands, r.candidateOf(ctx, w))
		}
		ws, we := ctxWindow(window.start, window.end)
		return resolutionOutcome{context: DiagnosticContext{
			Anchors: anchorsOf(anchor), Source: source,
			WindowStart: ws, WindowEnd: we,
			ResolutionStatus: ResolutionNeedChoice,
			ResolutionReason: "匹配到多个服务，请选择诊断目标",
		}, candidates: cands}, nil
	}
}

// resolvedFromWorkload 单一候选 → scoped 五步解析出 AuthorizedTarget。
func (r *ContextResolver) resolvedFromWorkload(ctx context.Context, anchor DiagnosticAnchor, source string, w scoped.WorkloadInfo, window timeWindow) (resolutionOutcome, error) {
	out, err := r.resolveWorkloadAnchor(ctx, DiagnosticAnchor{Kind: ContextKindWorkload, ID: w.ID})
	if err != nil {
		return resolutionOutcome{}, err
	}
	if out.context.ResolutionStatus != ResolutionResolved {
		return out, nil
	}
	// 还原来源锚点与窗口（窗口空=默认 now-30m）。
	anchors := anchorsOf(anchor)
	anchors = append(anchors, DiagnosticAnchor{Kind: ContextKindWorkload, ID: w.ID})
	out.context.Anchors = anchors
	out.context.Source = source
	if !window.isZero() {
		out.context.WindowStart, out.context.WindowEnd = ctxWindow(window.start, window.end)
	}
	return out, nil
}

// candidateOf 工作负载 → 候选描述（ID 由 service 层注册时生成绑定）。
func (r *ContextResolver) candidateOf(ctx context.Context, w scoped.WorkloadInfo) ContextCandidate {
	c := ContextCandidate{
		Kind: ContextKindWorkload, WorkloadID: w.ID, WorkloadName: w.Name,
		AppID: w.AppID, EnvID: w.EnvID, Service: w.Service, LaneID: w.LaneID,
	}
	if r.Targets != nil && r.Targets.Envs != nil {
		c.EnvName = r.Targets.Envs.EnvName(ctx, w.EnvID)
	}
	return c
}

// listServices 本租户 service 型工作负载（诊断目标域；job/cronjob 不在助手范围）。
func (r *ContextResolver) listServices(ctx context.Context) ([]scoped.WorkloadInfo, error) {
	if r.Workloads == nil {
		return nil, errors.New("workload lister 未装配")
	}
	wls, err := r.Workloads.ListWorkloads(ctx)
	if err != nil {
		return nil, err
	}
	return filterBy(wls, func(w scoped.WorkloadInfo) bool { return w.Type == "service" }), nil
}

func unavailableOutcome(anchor DiagnosticAnchor, source, reason string) resolutionOutcome {
	return resolutionOutcome{context: DiagnosticContext{
		Anchors: anchorsOf(anchor), Source: source,
		ResolutionStatus: ResolutionUnavailable, ResolutionReason: reason,
	}}
}

func anchorsOf(anchor DiagnosticAnchor) []DiagnosticAnchor {
	if anchor.Kind == "" {
		return nil
	}
	return []DiagnosticAnchor{anchor}
}

func filterBy(wls []scoped.WorkloadInfo, pred func(scoped.WorkloadInfo) bool) []scoped.WorkloadInfo {
	out := make([]scoped.WorkloadInfo, 0, len(wls))
	for _, w := range wls {
		if pred(w) {
			out = append(out, w)
		}
	}
	return out
}

// nameTokens 分词候选集合（空白 + 常见中英文标点；受控精确匹配用）。
var tokenSplitPattern = regexp.MustCompile(`[\s,，。；;:：、!！?？()（）\[\]【】"'']+`)

func nameTokens(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, t := range tokenSplitPattern.Split(text, -1) {
		if t = strings.TrimSpace(t); t != "" {
			tokens[t] = true
		}
	}
	return tokens
}

func clampWindow(start, end time.Time) timeWindow {
	now := time.Now().UTC()
	if start.IsZero() {
		start = now.Add(-defaultLookback)
	}
	if end.Before(start) {
		end = start
	}
	if end.After(now) {
		end = now
	}
	return timeWindow{start: start, end: end}
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
