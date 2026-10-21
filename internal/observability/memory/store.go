// Package memory 提供 observability.Repository 的内存实现（降级模式）。
//
// 去假数据：未接真实后端（PAAS_PROM_URL/PAAS_LOKI_URL/PAAS_JAEGER_URL 空）时
// metrics/logs/traces 返空，不惰性生成 mock 数据。告警规则（配置类）保留 seed 演示规则。
// 接真实后端走 internal/observability/real + compose 聚合，memory 仅作零依赖降级。
package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 observability.Repository（降级模式，无 mock 数据生成）。
type Store struct {
	mu       sync.Mutex
	rules    map[string]observability.AlertRule
	channels map[string]observability.NotificationChannel
	series   map[string]observability.MetricSeries // key = targetType|targetID|name
	logs     map[string][]observability.LogEntry   // tenantID -> 日志
	traces   map[string][]observability.Trace      // tenantID -> traces
	ruleSeq  int
	chSeq    int
}

func NewStore() *Store {
	s := &Store{
		rules:    map[string]observability.AlertRule{},
		channels: map[string]observability.NotificationChannel{},
		series:   map[string]observability.MetricSeries{},
		logs:     map[string][]observability.LogEntry{},
		traces:   map[string][]observability.Trace{},
	}
	s.seed()
	return s
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

// cloneTrace 深拷贝链路（Spans 切片 + Span.Tags map 逐层拷），确保返回值与 store 内独立，
// 防止并发读写 race（当前 traces 无写入 API 属降级防御，与 clonePoints 同款）。
func cloneTrace(t observability.Trace) observability.Trace {
	if t.Spans == nil {
		return t
	}
	cp := make([]observability.Span, len(t.Spans))
	for i, sp := range t.Spans {
		cp[i] = sp
		if sp.Tags != nil {
			m := make(map[string]string, len(sp.Tags))
			for k, v := range sp.Tags {
				m[k] = v
			}
			cp[i].Tags = m
		}
	}
	t.Spans = cp
	return t
}

func (s *Store) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.MetricSeries, 0)
	for _, series := range s.series {
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
		series.Points = clonePoints(series.Points)
		out = append(out, series)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.AlertRule, 0)
	for _, r := range s.rules {
		if r.TenantID == tid {
			out = append(out, cloneRule(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListAllAlertRules 跨租户列出全部告警规则（admin 平台总览，不过滤 tenant；按 TenantID 升序再 Name 升序）。
func (s *Store) ListAllAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, cloneRule(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	s.rules[rule.ID] = cloneRule(rule)
	return cloneRule(rule), nil
}

// UpdateAlertRule 更新规则（仅当前租户；ID/TenantID 不变，校验与 Create 同源）。
// 跨租户统一「规则不存在」不泄漏存在性。
func (s *Store) UpdateAlertRule(ctx context.Context, id string, rule observability.AlertRule) (observability.AlertRule, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return observability.AlertRule{}, err
	}
	if err := rule.Validate(); err != nil {
		return observability.AlertRule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.rules[id]
	if !ok || cur.TenantID != tid {
		return observability.AlertRule{}, fmt.Errorf("规则不存在: %s", id)
	}
	rule.ID = cur.ID
	rule.TenantID = cur.TenantID
	rule.UpdatedAt = time.Now()
	s.rules[id] = cloneRule(rule)
	return cloneRule(rule), nil
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
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

// cloneRule 深拷贝规则（ChannelIDs 切片），防返回值与内部状态共享底层数组。
func cloneRule(r observability.AlertRule) observability.AlertRule {
	if len(r.ChannelIDs) > 0 {
		r.ChannelIDs = append([]string(nil), r.ChannelIDs...)
	}
	return r
}

// cloneChannel 深拷贝渠道（Headers map）。
func cloneChannel(c observability.NotificationChannel) observability.NotificationChannel {
	if len(c.Headers) > 0 {
		h := make(map[string]string, len(c.Headers))
		for k, v := range c.Headers {
			h[k] = v
		}
		c.Headers = h
	}
	return c
}

// ListNotificationChannels 列出当前租户的通知渠道（按名称排序）。
func (s *Store) ListNotificationChannels(ctx context.Context) ([]observability.NotificationChannel, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.NotificationChannel, 0)
	for _, c := range s.channels {
		if c.TenantID == tid {
			out = append(out, cloneChannel(c))
		}
	}
	observability.SortChannels(out)
	return out, nil
}

// CreateNotificationChannel 创建渠道（ID 自动生成；ctx 租户为准；同租户重名拒绝，与 PG 唯一索引一致）。
func (s *Store) CreateNotificationChannel(ctx context.Context, ch observability.NotificationChannel) (observability.NotificationChannel, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	ch.Normalize()
	if err := ch.Validate(); err != nil {
		return observability.NotificationChannel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.channels {
		if c.TenantID == tid && c.Name == ch.Name {
			return observability.NotificationChannel{}, fmt.Errorf("通知渠道已存在: %s", ch.Name)
		}
	}
	s.chSeq++
	now := time.Now()
	ch.ID = fmt.Sprintf("ch-%d-%d", now.UnixNano(), s.chSeq)
	ch.TenantID = tid
	ch.CreatedAt, ch.UpdatedAt = now, now
	s.channels[ch.ID] = cloneChannel(ch)
	return cloneChannel(ch), nil
}

// UpdateNotificationChannel 更新渠道（仅当前租户；ID/TenantID/CreatedAt 不变；
// 敏感字段经 MergeSecretUpdates 保留未修改的原值——编辑不回填明文）。
func (s *Store) UpdateNotificationChannel(ctx context.Context, id string, ch observability.NotificationChannel) (observability.NotificationChannel, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.channels[id]
	if !ok || cur.TenantID != tid {
		return observability.NotificationChannel{}, fmt.Errorf("渠道不存在: %s", id)
	}
	ch = ch.MergeSecretUpdates(cur)
	ch.Normalize()
	if err := ch.Validate(); err != nil {
		return observability.NotificationChannel{}, err
	}
	for cid, c := range s.channels {
		if cid != id && c.TenantID == tid && c.Name == ch.Name {
			return observability.NotificationChannel{}, fmt.Errorf("通知渠道已存在: %s", ch.Name)
		}
	}
	ch.UpdatedAt = time.Now()
	s.channels[id] = cloneChannel(ch)
	return cloneChannel(ch), nil
}

// DeleteNotificationChannel 删除渠道（仅当前租户；跨租户统一 not found 不泄漏）。
// 引用该渠道的规则不级联删除——引擎解析时忽略不存在的渠道 ID，规则本身仍生效。
func (s *Store) DeleteNotificationChannel(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[id]
	if !ok || c.TenantID != tid {
		return fmt.Errorf("渠道不存在: %s", id)
	}
	delete(s.channels, id)
	return nil
}

// ListAlerts 即时评估 enabled 规则对匹配 series 当前值超阈值者生成 firing 告警。
// 降级模式 series 为空（无 mock 指标），返空；接真实后端时 series 由 real store 提供。
// targetType/targetId 非空时按维度过滤（仅返回匹配的 firing 告警）。
func (s *Store) ListAlerts(ctx context.Context, targetType, targetId string) ([]observability.Alert, error) {
	tid, err := tenant.IDOrErr(ctx)
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
		for _, series := range s.series {
			if series.TenantID != tid {
				continue
			}
			if !r.Matches(series) {
				continue
			}
			if r.Breached(series.Current) {
				// 维度过滤：targetType/targetId 非空时仅保留匹配的告警。
				if targetType != "" && series.TargetType != targetType {
					continue
				}
				if targetId != "" && series.TargetID != targetId {
					continue
				}
				alerts = append(alerts, observability.Alert{
					RuleID:     r.ID,
					RuleName:   r.Name,
					TargetType: series.TargetType,
					TargetID:   series.TargetID,
					MetricName: series.Name,
					Value:      series.Current,
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

// ListLogs 应用日志查询（过滤，不补点）。降级模式返空。按时间倒序返回。
// targetType 非空走 TargetType/TargetID 维度（dataservice/env/workload 通用）；否则走 appID 维度（向后兼容）。
// traceID 非空时按日志关联 trace 过滤（trace 展开关联日志）。
func (s *Store) ListLogs(ctx context.Context, appID, targetType, targetID, level, q, lane, traceID string, limit int) ([]observability.LogEntry, error) {
	_ = lane // mock 数据无 lane 维度（真实 Loki 路径消费）
	tid, err := tenant.IDOrErr(ctx)
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

	all := s.logs[tid]
	out := make([]observability.LogEntry, 0, len(all))
	qlower := strings.ToLower(q)
	for _, l := range all {
		// targetType 非空走 TargetType/TargetID 维度（dataservice/env/workload 通用匹配）；否则走 appID 维度（向后兼容）。
		if targetType != "" {
			if l.TargetType != targetType {
				continue
			}
			if targetID != "" && l.TargetID != targetID {
				continue
			}
		} else if appID != "" && l.AppID != appID {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		if qlower != "" && !strings.Contains(strings.ToLower(l.Message), qlower) {
			continue
		}
		if traceID != "" && l.TraceID != traceID {
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

// ListTraces 链路追踪查询（过滤，不补点）。降级模式返空。按 StartedAt 倒序返回。
func (s *Store) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	tid, err := tenant.IDOrErr(ctx)
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

	all := s.traces[tid]
	out := make([]observability.Trace, 0, len(all))
	for _, t := range all {
		if appID != "" && t.AppID != appID {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, cloneTrace(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetTrace 按 traceID 精确查单条（memory 路径：遍历租户 traces 按 ID 匹配）。
func (s *Store) GetTrace(ctx context.Context, traceID string) (observability.Trace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return observability.Trace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.traces[tid] {
		if t.ID == traceID {
			return cloneTrace(t), nil
		}
	}
	return observability.Trace{}, observability.ErrTraceNotFound
}

// ProbeMetricsQuality memory 路径恒「未接入」——mock 数据无采集过程可诊断，
// 不伪造探测结果（spec 2026-10-17 专项04 §5；接入引导文案与 compose 未注入分支一致）。
func (s *Store) ProbeMetricsQuality(ctx context.Context, targetType, targetID string) (observability.QualityReport, error) {
	return observability.QualityReport{
		Source: observability.QualitySourceMock,
		Note:   "未接入真实采集源（PAAS_PROM_URL 未配置），当前指标为演示数据；配置后此处置为采集覆盖探测",
	}, nil
}

// seed 灌平台预置告警规则（targetID 空=该维度全部实体，新应用自动纳入监控）；
// metrics/logs/traces 不 seed（去假数据，接真实后端或返空）。
func (s *Store) seed() {
	now := time.Now()
	s.rules["rule-acme-cpu"] = observability.AlertRule{
		ID: "rule-acme-cpu", TenantID: "t-acme", Name: "应用 CPU 偏高",
		MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning,
		Enabled: true, UpdatedAt: now,
	}
	s.rules["rule-acme-app-err"] = observability.AlertRule{
		ID: "rule-acme-app-err", TenantID: "t-acme", Name: "应用错误率超标",
		MetricName: observability.MetricErrorRate, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 5, Severity: observability.SeverityCritical,
		Enabled: true, UpdatedAt: now,
	}
	s.rules["rule-acme-app-latency"] = observability.AlertRule{
		ID: "rule-acme-app-latency", TenantID: "t-acme", Name: "应用 P95 延迟过高",
		MetricName: observability.MetricLatency, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 1000, Severity: observability.SeverityWarning,
		Enabled: true, UpdatedAt: now,
	}
	s.rules["rule-acme-ds-cpu"] = observability.AlertRule{
		ID: "rule-acme-ds-cpu", TenantID: "t-acme", Name: "数据服务 CPU 打满",
		MetricName: observability.MetricCPU, TargetType: observability.TargetDataservice,
		Operator: observability.OpGT, Threshold: 0.8, Severity: observability.SeverityCritical,
		Enabled: true, UpdatedAt: now,
	}
	s.rules["rule-acme-ds-mem"] = observability.AlertRule{
		ID: "rule-acme-ds-mem", TenantID: "t-acme", Name: "数据服务内存过高",
		MetricName: observability.MetricMem, TargetType: observability.TargetDataservice,
		Operator: observability.OpGT, Threshold: 1024, Severity: observability.SeverityWarning,
		Enabled: true, UpdatedAt: now,
	}
}
