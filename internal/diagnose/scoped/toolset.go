// ScopedToolset（spec §6.3）：用户侧工具白名单。与平台工具集的差异——
//   - 目标固定：ns/部署名来自 AuthorizedTarget，LLM 无任何作用域参数；
//   - 查询全构造：Loki 只接受 keyword/level 白名单参数，Prometheus 只提供
//     模板化指标（无任意 PromQL），杜绝提示注入构造跨租户查询；
//   - 归属校验：pod 日志/指标先验证 pod 属于目标 selector 集合；
//   - 字段白名单：workload spec 只返镜像/资源/探针类允许字段，不返 env/Secret；
//   - 链路首期降级：service.name 白名单不能证明跨租户重名归属，不提供工具
//     （覆盖表 missing/scope_unverifiable）。
package scoped

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/diagnose"
	"github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
)

// 依赖窄接口（装配层注入；nil = 该数据源未接入，工具返 error/not_configured）。
type (
	// K8sReader 复用平台诊断同款方法面（internal/diagnose/tools 已验证）。
	K8sReader interface {
		DeploymentSpec(ctx context.Context, ns, name string) (*controller.DeploymentSpecInfo, error)
		PodsBySelector(ctx context.Context, ns string, matchLabels map[string]string) ([]workload.RuntimeInstance, error)
		NamespaceEvents(ctx context.Context, ns, objectName string, sinceMin int, warningOnly bool) ([]controller.DiagEvent, error)
		RawPodLogs(ctx context.Context, ns, pod, container string, tail int64, previous bool, grep string) (string, error)
		HasLivePods(ctx context.Context, ns, deployment string) (bool, error)
	}
	// PromQuerier 即时查询（模板指标用）。
	PromQuerier interface {
		QueryInstant(ctx context.Context, promql string) ([]real.RawSample, error)
	}
	// LokiQuerier 原始日志查询（selector 由本包构造，非 LLM）。
	LokiQuerier interface {
		RawLoki(ctx context.Context, logql string, sinceMin, limit int) ([]real.RawLogLine, error)
	}
	// ReleaseLister 最近发布摘要（装配层桥接 devops repo）。
	ReleaseLister interface {
		// RecentByWorkload 返回该工作负载最近发布（时间倒序，≤5 条；ctx 租户过滤由实现保证）。
		RecentByWorkload(ctx context.Context, appID, workloadID string) ([]ReleaseLite, error)
	}
	// ReleaseLite 发布元数据（不含量凭证）。
	ReleaseLite struct {
		Version   string
		ImageTag  string
		Digest    string
		Rollback  bool
		CreatedAt time.Time
		By        string
	}
)

// Toolset ScopedToolset 本体。非并发安全——每 run 构造一个实例。
type Toolset struct {
	target                             diagnose.AuthorizedTarget
	window                             int // 固定回看窗口（分钟；run 创建时固定，防执行漂移）
	k8s                                K8sReader
	prom                               PromQuerier
	loki                               LokiQuerier
	repo                               ReleaseLister
	ledger                             *Ledger
	start, end                         time.Time
	focusPod, focusUID, focusContainer string
	cur                                string // 当前执行工具名（ledger 记账归属；单线程顺序执行）
}

// New 构造（window ≤0 默认 30）。
func New(t diagnose.AuthorizedTarget, windowMin int, k8s K8sReader, prom PromQuerier, loki LokiQuerier, repo ReleaseLister) *Toolset {
	if windowMin <= 0 {
		windowMin = 30
	}
	if nilInterface(k8s) {
		k8s = nil
	}
	if nilInterface(prom) {
		prom = nil
	}
	if nilInterface(loki) {
		loki = nil
	}
	if nilInterface(repo) {
		repo = nil
	}
	return &Toolset{target: t, window: windowMin, k8s: k8s, prom: prom, loki: loki, repo: repo, ledger: NewLedger()}
}

// Ledger 账本（executor 取证据快照与覆盖汇总）。
func (ts *Toolset) Ledger() *Ledger { return ts.ledger }

// —— agent.DiagnosticToolset 实现 ——

type registryEntry struct {
	name   string
	desc   string
	schema map[string]any
	fn     func(ctx context.Context, argsJSON string) string
}

func (ts *Toolset) entries() []registryEntry {
	return []registryEntry{
		{"get_workload_status", "读取目标工作负载当前运行状态：Deployment 期望/就绪副本、Pod 列表（状态/就绪/重启数/启动时间）与 conditions。无需参数。",
			map[string]any{"type": "object", "properties": map[string]any{}}, ts.invokeStatus},
		{"get_workload_spec", "读取目标工作负载部署规格：镜像、容器资源 requests/limits（limit 唯一真源）、端口。不包含环境变量与凭证。无需参数。",
			map[string]any{"type": "object", "properties": map[string]any{}}, ts.invokeSpec},
		{"get_k8s_events", "读取目标工作负载及其 Pod 的近期 K8s 事件（默认只取 Warning）。事件有 TTL，窗口内为空不代表无异常。",
			map[string]any{"type": "object", "properties": map[string]any{
				"warningOnly": map[string]any{"type": "boolean", "description": "默认 true 只看 Warning；false 含 Normal"},
			}}, ts.invokeEvents},
		{"get_pod_logs", "读取目标工作负载某 Pod 的容器日志。pod 省略时自动选第一个 Pod；previous=true 取上次终止容器（排查崩溃重启）。",
			map[string]any{"type": "object", "properties": map[string]any{
				"pod":       map[string]any{"type": "string", "description": "Pod 名（须属于目标工作负载）"},
				"container": map[string]any{"type": "string", "description": "容器名，缺省主容器"},
				"previous":  map[string]any{"type": "boolean", "description": "取上次终止的容器日志"},
				"keyword":   map[string]any{"type": "string", "description": "只返回包含该关键字的行"},
				"tailLines": map[string]any{"type": "integer", "description": "尾部行数，默认 200，上限 500"},
			}}, ts.invokePodLogs},
		{"query_loki", "按关键词/级别检索目标工作负载的日志（服务端限定命名空间与 Pod 前缀，不接受原始查询语句）。窗口内无日志不代表服务无输出。",
			map[string]any{"type": "object", "properties": map[string]any{
				"keyword":  map[string]any{"type": "string", "description": "日志行关键词过滤（如 error/panic/超时）"},
				"level":    map[string]any{"type": "string", "description": "级别过滤：error/warn/info"},
				"sinceMin": map[string]any{"type": "integer", "description": "时间窗（分钟），缺省用任务窗口"},
			}}, ts.invokeLoki},
		{"get_pod_metrics", "读取目标工作负载 Pod 的实时资源用量（CPU 核/内存 working set）与部署规格 limits 对比。pod 省略时返回全部 Pod 汇总。",
			map[string]any{"type": "object", "properties": map[string]any{
				"pod": map[string]any{"type": "string", "description": "Pod 名（须属于目标工作负载）"},
			}}, ts.invokePodMetrics},
		{"get_recent_release", "读取目标工作负载最近发布记录（版本/镜像/时间/是否回滚）。发布时间相关性不等于因果证明。",
			map[string]any{"type": "object", "properties": map[string]any{}}, ts.invokeRelease},
	}
}

// Handlers 注册表顺序（诊断 Agent 全量装载）。
func (ts *Toolset) Handlers() []string {
	es := ts.entries()
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.name
	}
	return out
}

// Lookup 单工具元数据与执行器。
func (ts *Toolset) Lookup(handler string) (meta agent.BuiltinToolMeta, invoke func(ctx context.Context, argsJSON string) (string, error), ok bool) {
	for _, e := range ts.entries() {
		if e.name != handler {
			continue
		}
		meta = agent.BuiltinToolMeta{Description: e.desc, Schema: e.schema}
		fn := e.fn
		return meta, func(ctx context.Context, argsJSON string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if !ValidateUID(ctx, ts.k8s, ts.target) {
				return "", fmt.Errorf("target_replaced: 工作负载 UID 已变化")
			}
			return fn(ctx, argsJSON), nil // 错误走四态信封，invoke 恒 nil error（与平台工具一致）
		}, true
	}
	return agent.BuiltinToolMeta{}, nil, false
}

// —— 信封（与平台工具同形态：LLM 已验证可消费）——

type envelope struct {
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
	Note   string `json:"note,omitempty"`
	EvID   string `json:"evidenceId"`
}

func (ts *Toolset) emit(status, reasonCode, note, summary string, data any, snippet string) string {
	evID := ts.ledger.Record(ts.cur, status, reasonCode, summary, snippet, ts.window)
	entries := ts.ledger.Snapshot()
	e := entries[len(entries)-1]
	b, _ := json.Marshal(map[string]any{"status": status, "data": safeValue(data), "note": MaskSensitive(note), "evidenceId": evID, "snippet": e.Snippet, "reasonCode": e.ReasonCode, "observedAt": e.ObservedAt})
	// 信封整体脱敏，模型和持久化账本使用相同证据；不截断 JSON 结构。
	text := string(b)
	if len(text) > MaxSnippetBytes {
		b, _ = json.Marshal(map[string]any{"status": status, "evidenceId": evID, "snippet": e.Snippet, "note": "结果过大，请按证据片段分析"})
	}
	text = string(b)
	return text
}

// toolName 记录当前工具名（ledger 记账归属；Toolset 单线程顺序执行）。
func (ts *Toolset) toolName(name string) { ts.cur = name }

// —— 工具实现 ——

func (ts *Toolset) invokeStatus(ctx context.Context, _ string) string {
	ts.toolName("get_workload_status")
	t := ts.target
	if ts.k8s == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "K8s 数据源未接入", "K8s 读取器未装配", nil, "")
	}
	spec, err := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "deployment 读取失败", nil, "")
	}
	if spec == nil {
		return ts.emit(StatusMissing, ReasonNotFound, "Deployment 不存在（可能从未部署或已删除）", "deployment 不存在", nil, "")
	}
	pods, err := ts.authorizedPods(ctx, spec)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "pod 列表读取失败", map[string]any{"replicas": spec.Replicas}, "")
	}
	type podView struct {
		Name, Status, Ready string
		Restarts            int
		StartedAt           string
		Reason              string
		Containers          []workload.ContainerState
		ReadyCondition      string
	}
	views := make([]podView, 0, len(pods))
	for _, p := range pods {
		started := ""
		if !p.StartedAt.IsZero() {
			started = p.StartedAt.UTC().Format(time.RFC3339)
		}
		readyCond := ""
		for _, c := range p.Conditions {
			if c.Type == "Ready" {
				readyCond = c.Status
			}
		}
		views = append(views, podView{Name: p.Name, Status: p.Status, Ready: p.Ready, Restarts: p.Restarts, StartedAt: started, ReadyCondition: readyCond, Reason: p.Reason, Containers: p.Containers})
	}
	data := map[string]any{
		"deployment": t.WorkloadID, "namespace": t.Namespace,
		"desiredReplicas": spec.Replicas, "podCount": len(pods), "pods": views,
	}
	sum := fmt.Sprintf("期望 %d 副本，pod %d 个", spec.Replicas, len(pods))
	if len(pods) > 0 {
		notReady := 0
		for _, v := range views {
			if v.ReadyCondition != "True" {
				notReady++
			}
		}
		sum += fmt.Sprintf("（未就绪 %d）", notReady)
	}
	return ts.emit(StatusOK, "", "Running ≠ Ready；就绪以 conditions.Ready=True 为准", sum, data, "")
}

func (ts *Toolset) invokeSpec(ctx context.Context, _ string) string {
	ts.toolName("get_workload_spec")
	t := ts.target
	if ts.k8s == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "K8s 数据源未接入", "K8s 读取器未装配", nil, "")
	}
	spec, err := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "deployment 读取失败", nil, "")
	}
	if spec == nil {
		return ts.emit(StatusMissing, ReasonNotFound, "Deployment 不存在", "deployment 不存在", nil, "")
	}
	// 字段白名单：只返镜像/资源（env/Secret/命令行不外泄）。
	type cView struct {
		Name     string                      `json:"name"`
		Image    string                      `json:"image"`
		Requests controller.ResourceSpecInfo `json:"requests"`
		Limits   controller.ResourceSpecInfo `json:"limits"`
	}
	cn := make([]cView, 0, len(spec.Containers))
	for _, c := range spec.Containers {
		cn = append(cn, cView{c.Name, c.Image, c.Requests, c.Limits})
	}
	data := map[string]any{"deployment": t.WorkloadID, "namespace": t.Namespace, "containers": cn}
	note := ""
	for _, c := range spec.Containers {
		if c.Limits.Memory == "" || c.Limits.CPU == "" {
			note = "存在容器未设置资源 limits（BestEffort/可被无限压制）"
		}
	}
	return ts.emit(StatusOK, "", note, fmt.Sprintf("%d 个容器", len(cn)), data, "")
}

func (ts *Toolset) invokeEvents(ctx context.Context, argsJSON string) string {
	ts.toolName("get_k8s_events")
	t := ts.target
	if ts.k8s == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "K8s 数据源未接入", "K8s 读取器未装配", nil, "")
	}
	args := parseArgs(argsJSON)
	warningOnly := args.boolOr("warningOnly", true)
	// owner 链限定：deployment 名 + 各 pod 名（拒绝 ns 全量事件）。
	spec, err := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "deployment 读取失败", nil, "")
	}
	objects := []string{t.WorkloadID}
	objectUIDs := map[string]string{}
	if spec != nil {
		objectUIDs[t.WorkloadID] = spec.UID
	}
	if spec != nil {
		if pods, perr := ts.authorizedPods(ctx, spec); perr == nil {
			for _, p := range pods {
				objects = append(objects, p.Name)
				objectUIDs[p.Name] = p.UID
			}
		}
	}
	seen := map[string]bool{}
	var evs []controller.DiagEvent
	for _, obj := range objects {
		since := ts.window
		if !ts.start.IsZero() {
			since = int(time.Since(ts.start).Minutes()) + 1
		}
		list, err := ts.k8s.NamespaceEvents(ctx, t.Namespace, obj, since, warningOnly)
		if err != nil {
			return ts.emit(StatusError, "", err.Error(), "事件读取失败", nil, "")
		}
		for _, e := range list {
			if expected := objectUIDs[obj]; expected != "" && e.UID != expected {
				continue
			}
			if !ts.start.IsZero() && (e.At.Before(ts.start) || e.At.After(ts.end)) {
				continue
			}
			key := fmt.Sprintf("%s/%s/%d", e.Object, e.Reason, e.At.Unix())
			if !seen[key] {
				seen[key] = true
				evs = append(evs, e)
			}
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].At.After(evs[j].At) })
	if len(evs) > 50 {
		evs = evs[:50]
	}
	if len(evs) == 0 {
		return ts.emit(StatusEmpty, "", "窗口内无事件（K8s 事件有 TTL，空≠无异常）", "无匹配事件", nil, "")
	}
	type evView struct {
		Type, Reason, Object, Message string
		Count                         int32
		At                            string
	}
	views := make([]evView, 0, len(evs))
	var sb strings.Builder
	for _, e := range evs {
		views = append(views, evView{e.Type, e.Reason, e.Object, MaskSensitive(e.Message), e.Count, e.At.UTC().Format(time.RFC3339)})
		sb.WriteString(e.Object + " " + e.Reason + " " + e.Message + "\n")
	}
	return ts.emit(StatusOK, "", "", fmt.Sprintf("%d 条事件", len(evs)), map[string]any{"events": views}, sb.String())
}

func (ts *Toolset) invokePodLogs(ctx context.Context, argsJSON string) string {
	ts.toolName("get_pod_logs")
	t := ts.target
	if ts.k8s == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "K8s 数据源未接入", "K8s 读取器未装配", nil, "")
	}
	args := parseArgs(argsJSON)
	pod, container := args.str("pod"), args.str("container")
	if pod == "" {
		pod = ts.focusPod
	}
	if container == "" {
		container = ts.focusContainer
	}
	previous := args.boolOr("previous", false)
	keyword, tail := args.str("keyword"), int64(args.intOr("tailLines", 200))
	// 归属校验：pod 必须属于目标 selector 集合（spec §6.3）。
	spec, err := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil || spec == nil {
		return ts.emit(StatusError, "", "目标 Deployment 不可读", "deployment 读取失败", nil, "")
	}
	pods, err := ts.authorizedPods(ctx, spec)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "pod 列表读取失败", nil, "")
	}
	if len(pods) == 0 {
		return ts.emit(StatusEmpty, "", "目标无运行 Pod", "无 pod", nil, "")
	}
	if pod == "" {
		pod = pods[0].Name // 未指定取第一个
	}
	if !podInList(pods, pod) {
		return ts.emit(StatusError, "", "pod 不属于目标工作负载", "非法 pod", nil, "")
	}
	if ts.focusPod == pod && ts.focusUID != "" {
		for _, p := range pods {
			if p.Name == pod && p.UID != ts.focusUID {
				return ts.emit(StatusError, "target_replaced", "实例已更换，请重新选择", "实例 UID 已变化", nil, "")
			}
		}
	}
	if container != "" {
		allowed := false
		for _, p := range pods {
			if p.Name == pod {
				for _, c := range p.Containers {
					if c.Name == container {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			return ts.emit(StatusError, "", "容器不属于目标实例", "非法容器", nil, "")
		}
	}
	if tail <= 0 || tail > 500 {
		tail = 200
	}
	expectedUID := ""
	for _, p := range pods {
		if p.Name == pod {
			expectedUID = p.UID
		}
	}
	logs, err := ts.k8s.RawPodLogs(ctx, t.Namespace, pod, container, tail, previous, keyword)
	if err != nil {
		note := "日志读取失败"
		if strings.Contains(err.Error(), "previous terminated container") {
			return ts.emit(StatusMissing, ReasonNotFound, "无上次终止容器（Pod 未重启过）", "previous 日志不存在", nil, "")

		}
		return ts.emit(StatusError, "", note+": "+err.Error(), "日志读取失败", nil, "")
	}
	if expectedUID != "" {
		current, e := ts.authorizedPods(ctx, spec)
		valid := false
		if e == nil {
			for _, p := range current {
				if p.Name == pod && p.UID == expectedUID {
					valid = true
				}
			}
		}
		if !valid {
			return ts.emit(StatusError, "target_replaced", "读取期间实例发生变化，结果已丢弃", "实例已变化", nil, "")
		}
	}
	if strings.TrimSpace(logs) == "" {
		note := "无日志输出"
		if keyword != "" {
			note = "无包含关键词 " + keyword + " 的日志行（关键词过窄或无输出）"
		}
		return ts.emit(StatusEmpty, "", note, "无匹配日志", nil, "")
	}
	return ts.emit(StatusOK, "", "", fmt.Sprintf("pod=%s %d 行", pod, len(strings.Split(logs, "\n"))), map[string]any{"pod": pod, "previous": previous, "lines": countLines(logs)}, logs)
}

func (ts *Toolset) invokeLoki(ctx context.Context, argsJSON string) string {
	ts.toolName("query_loki")
	t := ts.target
	if ts.loki == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "日志检索源未接入（PAAS_LOKI_URL 未配置）", "Loki 未接入", nil, "")
	}
	args := parseArgs(argsJSON)
	keyword, level := args.str("keyword"), args.str("level")
	sinceMin := ts.window
	// 服务端构造 selector（LLM 无 logql 参数）：ns + 目标 pod 前缀（workload ID 开头）。
	if ts.k8s == nil {
		return ts.emit(StatusMissing, ReasonScopeUnverifiable, "无法验证日志所属实例", "归属无法确认", nil, "")
	}
	spec, specErr := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if specErr != nil || spec == nil {
		return ts.emit(StatusMissing, ReasonScopeUnverifiable, "工作负载不可读，无法验证日志归属", "归属无法确认", nil, "")
	}
	pods, podErr := ts.authorizedPods(ctx, spec)
	if podErr != nil || len(pods) == 0 {
		return ts.emit(StatusMissing, ReasonScopeUnverifiable, "没有可验证的当前实例，不能扩大到历史名称前缀检索", "归属无法确认", nil, "")
	}
	names := []string{}
	allowedPods := map[string]bool{}
	for _, p := range pods {
		names = append(names, regexp.QuoteMeta(p.Name))
		allowedPods[p.Name] = true
	}
	logql := fmt.Sprintf(`{namespace=%q,pod=~%q}`, t.Namespace, "^("+strings.Join(names, "|")+")$")
	if level != "" {
		logql += fmt.Sprintf(` |~ "(?i)%s"`, regexp.QuoteMeta(level))
	}
	if keyword != "" {
		logql += fmt.Sprintf(` |= %q`, keyword)
	}
	var lines []real.RawLogLine
	var err error
	if reader, ok := ts.loki.(interface {
		RawLokiWindow(context.Context, string, time.Time, time.Time, int) ([]real.RawLogLine, error)
	}); ok && !ts.start.IsZero() {
		lines, err = reader.RawLokiWindow(ctx, logql, ts.start, ts.end, 300)
	} else {
		lines, err = ts.loki.RawLoki(ctx, logql, sinceMin, 300)
	}
	if !ts.start.IsZero() {
		filtered := lines[:0]
		for _, line := range lines {
			if allowedPods[line.Pod] && !line.TS.Before(ts.start) && !line.TS.After(ts.end) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "Loki 查询失败", nil, "")
	}
	if len(lines) == 0 {

		return ts.emit(StatusEmpty, "", "窗口内无日志（Pod 刚启动/无输出/窗口不含故障时刻）", "无日志", nil, "")
	}
	var sb strings.Builder
	for _, l := range lines {
		if !allowedPods[l.Pod] {
			continue
		}
		sb.WriteString(l.TS.UTC().Format(time.RFC3339) + " " + l.Line + "\n")
	}
	return ts.emit(StatusOK, "", "", fmt.Sprintf("%d 行", len(lines)), map[string]any{"lines": len(lines)}, sb.String())
}

func (ts *Toolset) invokePodMetrics(ctx context.Context, argsJSON string) string {
	ts.toolName("get_pod_metrics")
	t := ts.target
	if ts.prom == nil || ts.k8s == nil {
		return ts.emit(StatusError, ReasonNotConfigured, "指标源未接入（PAAS_PROM_URL 未配置）", "Prometheus 未接入", nil, "")
	}
	args := parseArgs(argsJSON)
	pod := args.str("pod")
	spec, err := ts.k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil || spec == nil {
		return ts.emit(StatusError, "", "目标 Deployment 不可读", "deployment 读取失败", nil, "")
	}
	pods, err := ts.authorizedPods(ctx, spec)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "pod 列表读取失败", nil, "")
	}
	targets := pods
	if pod != "" {
		if !podInList(pods, pod) {
			return ts.emit(StatusError, "", "pod 不属于目标工作负载", "非法 pod", nil, "")
		}
		targets = []workload.RuntimeInstance{{Instance: workload.Instance{Name: pod}}}
	}
	type mView struct {
		Pod      string  `json:"pod"`
		CPUCores float64 `json:"cpuCores"`
		MemBytes float64 `json:"memWorkingSetBytes"`
	}
	views := make([]mView, 0, len(targets))
	for _, p := range targets {
		lbl := fmt.Sprintf(`{namespace=%q,pod=%q,image!=""}`, t.Namespace, p.Name)
		cpuQ := fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total%s[5m]))", lbl)
		memQ := fmt.Sprintf("sum(container_memory_working_set_bytes%s)", lbl)
		cpu, cpuErr := ts.prom.QueryInstant(ctx, cpuQ)
		mem, memErr := ts.prom.QueryInstant(ctx, memQ)
		if cpuErr != nil || memErr != nil {
			return ts.emit(StatusError, "", "指标查询失败: "+cpuErr.Error(), "Prometheus 查询失败", nil, "")
		}
		if len(cpu) == 0 || len(mem) == 0 {
			return ts.emit(StatusEmpty, "", "CPU 或内存无采样，不能当作零值", "缺少指标采样", nil, "")
		}
		v := mView{Pod: p.Name}
		if cpuErr == nil {
			for _, s := range cpu {
				v.CPUCores += s.Value
			}
		}
		if memErr == nil {
			for _, s := range mem {
				v.MemBytes += s.Value
			}
		}
		views = append(views, v)
	}
	if len(views) == 0 {
		return ts.emit(StatusEmpty, "", "目标无 Pod", "无 pod", nil, "")
	}
	// specLimits 白名单字段（limit 真源=部署规格）。
	type cView struct {
		Name   string                      `json:"name"`
		Limits controller.ResourceSpecInfo `json:"limits"`
	}
	limits := make([]cView, 0, len(spec.Containers))
	for _, c := range spec.Containers {
		limits = append(limits, cView{c.Name, c.Limits})
	}
	data := map[string]any{"pods": views, "specLimits": limits}
	return ts.emit(StatusOK, "", "limit 以 specLimits（部署规格）为准；空序列≠零值", fmt.Sprintf("%d 个 Pod 用量", len(views)), data, "")
}

func (ts *Toolset) invokeRelease(ctx context.Context, _ string) string {
	ts.toolName("get_recent_release")
	t := ts.target
	if ts.repo == nil {
		return ts.emit(StatusMissing, ReasonNotConfigured, "发布数据未接入", "发布仓储未装配", nil, "")
	}
	rels, err := ts.repo.RecentByWorkload(ctx, t.AppID, t.WorkloadID)
	if err != nil {
		return ts.emit(StatusError, "", err.Error(), "发布记录读取失败", nil, "")
	}
	if len(rels) == 0 {
		return ts.emit(StatusEmpty, "", "无发布记录（从未经流水线发布）", "无发布", nil, "")
	}
	type rView struct {
		Version, ImageTag, Digest, By string
		Rollback                      bool
		CreatedAt                     string
	}
	views := make([]rView, 0, len(rels))
	for _, r := range rels {
		views = append(views, rView{r.Version, r.ImageTag, r.Digest, r.By, r.Rollback, r.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return ts.emit(StatusOK, "", "发布时间相关≠因果证明", fmt.Sprintf("最近 %d 次发布", len(rels)), map[string]any{"releases": views}, "")
}

// —— helpers ——

func podInList(pods []workload.RuntimeInstance, name string) bool {
	for _, p := range pods {
		if p.Name == name {
			return true
		}
	}
	return false
}

func countLines(s string) int { return len(strings.Split(s, "\n")) }

// args 解析（permissive：坏 JSON 按空参；与平台工具一致）。
type toolArgs map[string]any

func parseArgs(jsonStr string) toolArgs {
	m := toolArgs{}
	if jsonStr == "" {
		return m
	}
	_ = json.Unmarshal([]byte(jsonStr), &m)
	return m
}

func (a toolArgs) str(k string) string {
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

func (a toolArgs) intOr(k string, d int) int {
	if v, ok := a[k].(float64); ok {
		return int(v)
	}
	return d
}

func (a toolArgs) boolOr(k string, d bool) bool {
	if v, ok := a[k].(bool); ok {
		return v
	}
	return d
}

// SetWindow 固定本轮历史查询窗口，LLM 不能扩大窗口。
func (ts *Toolset) SetWindow(start, end time.Time) { ts.start = start; ts.end = end }

// SetFocus 绑定已经校验的实例，避免默认读取第一个 Pod。
func (ts *Toolset) SetFocus(pod, uid, container string) {
	ts.focusPod = pod
	ts.focusUID = uid
	ts.focusContainer = container
}
func nilInterface(v any) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil())
}

func safeValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var raw any
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	return redactValue(raw)
}
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return MaskSensitive(x)
	case map[string]any:
		for k, w := range x {
			x[k] = redactValue(w)
		}
	case []any:
		for i, w := range x {
			x[i] = redactValue(w)
		}
	}
	return v
}

// authorizedPods 将 selector 候选与真实 owner 链交叉验证，名称前缀不作为授权依据。
func (ts *Toolset) authorizedPods(ctx context.Context, spec *controller.DeploymentSpecInfo) ([]workload.RuntimeInstance, error) {
	pods, err := ts.k8s.PodsBySelector(ctx, ts.target.Namespace, spec.Selector)
	if err != nil {
		return nil, err
	}
	if reader, ok := ts.k8s.(interface {
		PodWorkloadRef(context.Context, string, string) (*controller.DeploymentSpecInfo, error)
	}); ok {
		kept := []workload.RuntimeInstance{}
		for _, p := range pods {
			owner, e := reader.PodWorkloadRef(ctx, ts.target.Namespace, p.Name)
			if e != nil {
				return nil, e
			}
			if owner != nil && owner.Name == ts.target.WorkloadID && owner.UID == spec.UID {
				kept = append(kept, p)
			}
		}
		pods = kept
	}
	return pods, nil
}
