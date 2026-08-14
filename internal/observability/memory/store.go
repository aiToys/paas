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
	mu      sync.Mutex
	rules   map[string]observability.AlertRule
	series  map[string]observability.MetricSeries // key = targetType|targetID|name
	logs    map[string][]observability.LogEntry   // tenantID -> 日志
	traces  map[string][]observability.Trace      // tenantID -> traces
	ruleSeq int
}

func NewStore() *Store {
	s := &Store{
		rules:  map[string]observability.AlertRule{},
		series: map[string]observability.MetricSeries{},
		logs:   map[string][]observability.LogEntry{},
		traces: map[string][]observability.Trace{},
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
			out = append(out, r)
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
		out = append(out, r)
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
	s.rules[rule.ID] = rule
	return rule, nil
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
// targetType=dataservice 走 TargetType/TargetID 维度；否则走原 appID 维度（向后兼容）。
func (s *Store) ListLogs(ctx context.Context, appID, targetType, targetID, level, q string, limit int) ([]observability.LogEntry, error) {
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
		// targetType=dataservice 走 TargetType/TargetID 维度；否则走原 appID 维度（向后兼容）。
		if targetType == observability.TargetDataservice {
			if l.TargetType != observability.TargetDataservice {
				continue
			}
			if targetID != "" && l.TargetID != targetID {
				continue
			}
		} else {
			if appID != "" && l.AppID != appID {
				continue
			}
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
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// seed 仅灌告警规则（配置类演示）；metrics/logs/traces 不 seed（去假数据，接真实后端或返空）。
func (s *Store) seed() {
	now := time.Now()
	s.rules["rule-acme-cpu"] = observability.AlertRule{
		ID: "rule-acme-cpu", TenantID: "t-acme", Name: "应用 CPU 偏高",
		MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning,
		Enabled: true, UpdatedAt: now,
	}
}
