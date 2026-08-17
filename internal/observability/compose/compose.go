// Package compose 聚合 observability 的多源 reader 为单一 Repository：
// alert rules 始终委托 memory（规则配置），metrics/logs/traces 委托任意 reader
// （real 真实后端或 memory 惰性 mock，可混用）。ListAlerts 基于 metrics reader 即时评估。
package compose

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// Repo 聚合多源 reader 实现 observability.Repository。
type Repo struct {
	rules   observability.RuleStore
	metrics observability.MetricsReader
	logs    observability.LogsReader
	traces  observability.TracesReader
}

// New 创建聚合 Repository。rules 始终是 memory 规则存储；metrics/logs/traces 可为 real 或 memory。
func New(rules observability.RuleStore, metrics observability.MetricsReader, logs observability.LogsReader, traces observability.TracesReader) *Repo {
	return &Repo{rules: rules, metrics: metrics, logs: logs, traces: traces}
}

func (r *Repo) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	return r.metrics.ListMetrics(ctx, targetType, targetID, name)
}

func (r *Repo) ListLogs(ctx context.Context, appID, targetType, targetID, level, q, lane string, limit int) ([]observability.LogEntry, error) {
	return r.logs.ListLogs(ctx, appID, targetType, targetID, level, q, lane, limit)
}

func (r *Repo) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	return r.traces.ListTraces(ctx, appID, status, limit)
}

func (r *Repo) GetTrace(ctx context.Context, traceID string) (observability.Trace, error) {
	return r.traces.GetTrace(ctx, traceID)
}

func (r *Repo) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	return r.rules.ListAlertRules(ctx)
}

func (r *Repo) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	return r.rules.CreateAlertRule(ctx, rule)
}

func (r *Repo) DeleteAlertRule(ctx context.Context, id string) error {
	return r.rules.DeleteAlertRule(ctx, id)
}

// ListAllAlertRules 跨租户列出全部告警规则（admin 平台总览，委托 rules store）。
func (r *Repo) ListAllAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	return r.rules.ListAllAlertRules(ctx)
}

// ListAlerts 即时评估：遍历 rules，对每 enabled 规则调 metrics reader 取匹配 series 当前值评估。
// real 模式 metrics 来自 Prometheus，memory 模式来自 mock seed——统一在此评估。
// targetType/targetId 非空时按维度过滤（仅返回匹配的 firing 告警）。
func (r *Repo) ListAlerts(ctx context.Context, targetType, targetId string) ([]observability.Alert, error) {
	rules, err := r.rules.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	series, err := r.metrics.ListMetrics(ctx, "", "", "")
	if err != nil {
		log.Printf("observability compose ListAlerts: 取 metrics 失败: %v", err)
		series = nil
	}
	alerts := make([]observability.Alert, 0)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, s := range series {
			if !rule.Matches(s) {
				continue
			}
			if rule.Breached(s.Current) {
				// 维度过滤：targetType/targetId 非空时仅保留匹配的告警。
				if targetType != "" && s.TargetType != targetType {
					continue
				}
				if targetId != "" && s.TargetID != targetId {
					continue
				}
				alerts = append(alerts, observability.Alert{
					RuleID:     rule.ID,
					RuleName:   rule.Name,
					TargetType: s.TargetType,
					TargetID:   s.TargetID,
					MetricName: s.Name,
					Value:      s.Current,
					Threshold:  rule.Threshold,
					Operator:   rule.Operator,
					Severity:   rule.Severity,
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
