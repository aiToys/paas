// Package memory 提供 observability.Repository 的内存实现。
// 惰性时序：查询时按当前时间补点（随机游走），模拟数据面采集；无后台 goroutine。
package memory

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

const sampleInterval = 5 * time.Second // 采样间隔
const logInterval = 8 * time.Second    // 日志补点间隔
const traceInterval = 20 * time.Second // trace 补点间隔

// logTemplates mock 日志模板池（按级别分组），惰性补点时随机抽取。
var logTemplates = []struct {
	Level   string
	Message string
}{
	{observability.LevelInfo, "GET /v1/chat/completions 200 (84ms)"},
	{observability.LevelInfo, "模型路由 qwen2.5-7b -> channel-primary"},
	{observability.LevelInfo, "滚动更新 pod 3/3 ready"},
	{observability.LevelWarn, "GPU 显存使用率 82%，接近阈值"},
	{observability.LevelWarn, "请求队列堆积 12，触发限流"},
	{observability.LevelWarn, "镜像拉取重试 2/5"},
	{observability.LevelError, "上游 provider 超时，降级备用通道"},
	{observability.LevelError, "健康检查失败，实例标记 unhealthy"},
	{observability.LevelError, "db 连接池耗尽，请求排队"},
}

// traceTemplates mock 链路模板池：入口操作 + 调用链 spans。
var traceTemplates = []struct {
	Operation string
	Spans     []struct {
		Operation, Service string
	}
}{
	{"POST /v1/chat/completions", []struct{ Operation, Service string }{
		{"gateway.authenticate", "api-gateway"},
		{"router.resolveChannel", "maas-router"},
		{"provider.chatCompletion", "vllm-provider"},
	}},
	{"GET /api/applications", []struct{ Operation, Service string }{
		{"gateway.authenticate", "api-gateway"},
		{"app.list", "app-service"},
		{"db.query", "postgres"},
	}},
	{"POST /api/releases", []struct{ Operation, Service string }{
		{"gateway.authenticate", "api-gateway"},
		{"release.orchestrate", "devops-svc"},
		{"workload.applyImage", "workload-svc"},
		{"db.update", "postgres"},
	}},
	{"POST /api/dataservices", []struct{ Operation, Service string }{
		{"gateway.authenticate", "api-gateway"},
		{"dataservice.provision", "ds-svc"},
		{"db.insert", "postgres"},
	}},
}

// Store 实现 observability.Repository。
type Store struct {
	mu        sync.Mutex
	rules     map[string]observability.AlertRule
	series    map[string]observability.MetricSeries // key = targetType|targetID|name
	logs      map[string][]observability.LogEntry   // tenantID -> 日志（最新在尾）
	lastLog   map[string]time.Time                  // tenantID -> 最后补点时间
	logApps   map[string][]string                   // tenantID -> 应用 id 池（日志/trace 归属）
	traces    map[string][]observability.Trace      // tenantID -> traces
	lastTrace map[string]time.Time                  // tenantID -> 最后 trace 补点时间
	rng       *rand.Rand
	ruleSeq   int
	logSeq    int
	traceSeq  int
}

func NewStore() *Store {
	s := &Store{
		rules:     map[string]observability.AlertRule{},
		series:    map[string]observability.MetricSeries{},
		logs:      map[string][]observability.LogEntry{},
		lastLog:   map[string]time.Time{},
		logApps:   map[string][]string{},
		traces:    map[string][]observability.Trace{},
		lastTrace: map[string]time.Time{},
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // G404 误报：mock 模拟数据生成，非安全场景
	}
	s.seed()
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

// rangeBounds 返回 metric 的合理值域 [min,max]（游走边界）。
func rangeBounds(name string) (float64, float64) {
	switch name {
	case observability.MetricCPU, observability.MetricMem, observability.MetricErrorRate:
		return 0, 100
	case observability.MetricRPS:
		return 0, 10000
	case observability.MetricLatency:
		return 0, 2000
	default:
		return 0, 1000
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// advance 惰性补点：返回补点后的 series（Points 全新分配，不修改入参底层数组，
// 避免与 store 内 series 共享导致的并发 race 与历史点位污染）。Current 随机游走。
func (s *Store) advance(series observability.MetricSeries) observability.MetricSeries {
	now := time.Now()
	if len(series.Points) == 0 {
		series.Points = []observability.MetricPoint{{TS: now, Value: series.Current}}
		return series
	}
	// 全新 slice 拷贝历史点，预留容量避免再次扩容。
	pts := make([]observability.MetricPoint, len(series.Points), len(series.Points)+observability.MaxPoints)
	copy(pts, series.Points)
	last := pts[len(pts)-1].TS
	// 冷门 target 长时间未查时（间隔 > MaxPoints*interval），从最近窗口重新起算，
	// 避免"每次最多补 MaxPoints 个点"永远追不上 now 导致最新点持续滞后。
	if maxAge := time.Duration(observability.MaxPoints) * sampleInterval; now.Sub(last) > maxAge {
		last = now.Add(-maxAge)
		pts = pts[len(pts)-1:] // 仅保留最后一个历史点作为游走起点
	}
	lo, hi := rangeBounds(series.Name)
	span := hi - lo
	cur := series.Current
	for i := 0; i < observability.MaxPoints; i++ {
		if last.Add(sampleInterval).After(now) {
			break
		}
		last = last.Add(sampleInterval)
		delta := (s.rng.Float64() - 0.5) * span * 0.15 // ±7.5% 区间幅度
		cur = clamp(cur+delta, lo, hi)
		pts = append(pts, observability.MetricPoint{TS: last, Value: cur})
	}
	// 环形截断
	if len(pts) > observability.MaxPoints {
		pts = pts[len(pts)-observability.MaxPoints:]
	}
	series.Points = pts
	series.Current = cur
	return series
}

// clonePoints 返回 Points 切片的深拷贝，确保返回值与 store 内底层数组独立。
func clonePoints(pts []observability.MetricPoint) []observability.MetricPoint {
	if len(pts) == 0 {
		return nil
	}
	cp := make([]observability.MetricPoint, len(pts))
	copy(cp, pts)
	return cp
}

func (s *Store) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.MetricSeries, 0)
	for key, series := range s.series {
		if series.TenantID != tid {
			continue
		}
		if targetType != "" && series.TargetType != targetType {
			continue
		}
		if targetID != "" && series.TargetID != targetID {
			continue
		}
		if name != "" && series.Name != name {
			continue
		}
		advanced := s.advance(series)
		s.series[key] = advanced // 写回 store（独立底层数组）
		// 返回前再次深拷贝 Points，避免 out 与 store 共享（Unlock 后 Encode 并发安全）。
		advanced.Points = clonePoints(advanced.Points)
		out = append(out, advanced)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.AlertRule, 0)
	for _, r := range s.rules {
		if r.TenantID == tid {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return observability.AlertRule{}, err
	}
	if err := rule.Validate(); err != nil {
		return observability.AlertRule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ruleSeq++
	rule.ID = fmt.Sprintf("rule-%d-%d", time.Now().UnixNano(), s.ruleSeq)
	rule.TenantID = tid
	rule.UpdatedAt = time.Now()
	s.rules[rule.ID] = rule
	return rule, nil
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rules[id]
	if !ok || r.TenantID != tid {
		return fmt.Errorf("规则不存在: %s", id)
	}
	delete(s.rules, id)
	return nil
}

// ListAlerts 即时评估所有 enabled 规则，对匹配 series 的当前值超阈值者生成 firing 告警。
func (s *Store) ListAlerts(ctx context.Context) ([]observability.Alert, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	alerts := make([]observability.Alert, 0)
	for _, r := range s.rules {
		if r.TenantID != tid || !r.Enabled {
			continue
		}
		for key, series := range s.series {
			if series.TenantID != tid {
				continue
			}
			if !r.Matches(series) {
				continue
			}
			// 评估前补点到最新值（advance 返回独立 series，写回 store 持久化）。
			cur := s.advance(series)
			s.series[key] = cur
			if r.Breached(cur.Current) {
				alerts = append(alerts, observability.Alert{
					RuleID:     r.ID,
					RuleName:   r.Name,
					TargetType: cur.TargetType,
					TargetID:   cur.TargetID,
					MetricName: cur.Name,
					Value:      cur.Current,
					Threshold:  r.Threshold,
					Operator:   r.Operator,
					Severity:   r.Severity,
					Status:     "firing",
					FiredAt:    time.Now(),
				})
			}
		}
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity == observability.SeverityCritical
		}
		return alerts[i].RuleName < alerts[j].RuleName
	})
	return alerts, nil
}

// seed 跨两租户指标 series + 告警规则。
// ListLogs 应用日志查询（惰性补点 + 过滤）。按时间倒序返回。
func (s *Store) ListLogs(ctx context.Context, appID, level, q string, limit int) ([]observability.LogEntry, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if level != "" && !observability.ValidLevel(level) {
		return nil, fmt.Errorf("非法级别: %s", level)
	}
	if limit <= 0 || limit > observability.MaxLogs {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.appendLogs(tid)

	all := s.logs[tid]
	out := make([]observability.LogEntry, 0, len(all))
	qlower := strings.ToLower(q)
	for _, l := range all {
		if appID != "" && l.AppID != appID {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		if qlower != "" && !strings.Contains(strings.ToLower(l.Message), qlower) {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// appendLogs 惰性补点：按距上次补点时间的间隔数追加 mock 日志，环形截断 MaxLogs。
func (s *Store) appendLogs(tid string) {
	apps := s.logApps[tid]
	if len(apps) == 0 {
		return
	}
	now := time.Now()
	last, ok := s.lastLog[tid]
	if !ok {
		last = now.Add(-40 * time.Second) // 首次查询假定 40s 前，补约 5 条
	}
	n := int(now.Sub(last) / logInterval)
	if n <= 0 {
		return
	}
	if n > 10 {
		n = 10
	}
	step := (now.Sub(last)) / time.Duration(n)
	for i := 0; i < n; i++ {
		tpl := logTemplates[s.rng.Intn(len(logTemplates))]
		app := apps[s.rng.Intn(len(apps))]
		s.logSeq++
		s.logs[tid] = append(s.logs[tid], observability.LogEntry{
			ID:        fmt.Sprintf("log-%s-%d", tid, s.logSeq),
			TenantID:  tid,
			AppID:     app,
			Level:     tpl.Level,
			Message:   tpl.Message,
			TraceID:   fmt.Sprintf("%x", s.rng.Uint64()),
			Timestamp: last.Add(step * time.Duration(i+1)),
		})
	}
	s.lastLog[tid] = now
	if len(s.logs[tid]) > observability.MaxLogs {
		s.logs[tid] = s.logs[tid][len(s.logs[tid])-observability.MaxLogs:]
	}
}

// ListTraces 链路追踪查询（惰性补点 + 过滤）。按 StartedAt 倒序返回。
func (s *Store) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if status != "" && !observability.ValidTraceStatus(status) {
		return nil, fmt.Errorf("非法 trace 状态: %s", status)
	}
	if limit <= 0 || limit > observability.MaxTraces {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.appendTraces(tid)

	all := s.traces[tid]
	out := make([]observability.Trace, 0, len(all))
	for _, t := range all {
		if appID != "" && t.AppID != appID {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// appendTraces 惰性补点：按距上次补点时间的间隔生成 mock trace，环形截断 MaxTraces。
func (s *Store) appendTraces(tid string) {
	apps := s.logApps[tid]
	if len(apps) == 0 {
		return
	}
	now := time.Now()
	last, ok := s.lastTrace[tid]
	if !ok {
		last = now.Add(-3 * time.Minute) // 首次查询假定 3 分钟前
	}
	n := int(now.Sub(last) / traceInterval)
	if n <= 0 {
		return
	}
	if n > 6 {
		n = 6
	}
	step := now.Sub(last) / time.Duration(n)
	for i := 0; i < n; i++ {
		tpl := traceTemplates[s.rng.Intn(len(traceTemplates))]
		app := apps[s.rng.Intn(len(apps))]
		s.traceSeq++
		status := observability.TraceSuccess
		tags := map[string]string{"http.status": "200"}
		if s.rng.Intn(5) == 0 { // ~20% 错误
			status = observability.TraceError
			tags = map[string]string{"error": "upstream timeout"}
		}
		spans := make([]observability.Span, 0, len(tpl.Spans))
		var startMs int64
		var prevID string
		for idx, sp := range tpl.Spans {
			dur := int64(20 + s.rng.Intn(180))
			spans = append(spans, observability.Span{
				ID:         fmt.Sprintf("span-%s-%d-%d", tid, s.traceSeq, idx),
				ParentID:   prevID,
				Operation:  sp.Operation,
				Service:    sp.Service,
				StartMs:    startMs,
				DurationMs: dur,
				Tags:       tags,
			})
			prevID = fmt.Sprintf("span-%s-%d-%d", tid, s.traceSeq, idx)
			startMs += dur
		}
		s.traces[tid] = append(s.traces[tid], observability.Trace{
			ID:         fmt.Sprintf("trace-%s-%d", tid, s.traceSeq),
			TenantID:   tid,
			AppID:      app,
			Operation:  tpl.Operation,
			Status:     status,
			DurationMs: startMs,
			StartedAt:  last.Add(step * time.Duration(i+1)),
			Spans:      spans,
		})
	}
	s.lastTrace[tid] = now
	if len(s.traces[tid]) > observability.MaxTraces {
		s.traces[tid] = s.traces[tid][len(s.traces[tid])-observability.MaxTraces:]
	}
}

func (s *Store) seed() {
	now := time.Now()
	mk := func(tid, tt, tid2, name, unit string, current float64) {
		key := tt + "|" + tid2 + "|" + name
		s.series[key] = observability.MetricSeries{
			ID:         "ms-" + tid + "-" + tt + "-" + tid2 + "-" + name,
			TenantID:   tid,
			TargetType: tt,
			TargetID:   tid2,
			Name:       name,
			Unit:       unit,
			Current:    current,
			Points:     []observability.MetricPoint{{TS: now, Value: current}},
		}
	}
	// acme app-cs 四项指标
	mk("t-acme", observability.TargetApp, "app-cs", observability.MetricCPU, "%", 62)
	mk("t-acme", observability.TargetApp, "app-cs", observability.MetricMem, "%", 55)
	mk("t-acme", observability.TargetApp, "app-cs", observability.MetricRPS, "req/s", 320)
	mk("t-acme", observability.TargetApp, "app-cs", observability.MetricLatency, "ms", 120)
	// acme app-rec
	mk("t-acme", observability.TargetApp, "app-rec", observability.MetricCPU, "%", 48)
	mk("t-acme", observability.TargetApp, "app-rec", observability.MetricMem, "%", 40)
	// globex app-agent
	mk("t-globex", observability.TargetApp, "app-agent", observability.MetricCPU, "%", 70)
	mk("t-globex", observability.TargetApp, "app-agent", observability.MetricMem, "%", 60)
	mk("t-globex", observability.TargetApp, "app-agent", observability.MetricRPS, "req/s", 180)

	// 告警规则：cpu>50 warning（acme app-cs 常触发，演示用）
	s.rules["rule-acme-cpu"] = observability.AlertRule{
		ID: "rule-acme-cpu", TenantID: "t-acme", Name: "应用 CPU 偏高",
		MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning,
		Enabled: true, UpdatedAt: now,
	}

	// 日志归属应用池（与 application seed 对齐，便于按应用过滤）
	s.logApps["t-acme"] = []string{"app-cs", "app-rec", "app-lab"}
	s.logApps["t-globex"] = []string{"app-etl", "app-agent"}
}
