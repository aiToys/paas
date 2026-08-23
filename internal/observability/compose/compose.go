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
	// engine 评估引擎（R4-C2 后台评估 + 状态机 + webhook 出站）。
	// 注入后 ListAlerts 读引擎快照；nil 降级即时评估（现状行为）。
	engine AlertLister
}

// AlertLister 评估引擎的最小消费接口（避免 compose→alert 循环 import）。
type AlertLister interface {
	ListAlerts(ctx context.Context, targetType, targetId string) ([]observability.Alert, error)
}

// New 创建聚合 Repository。rules 始终是 memory 规则存储；metrics/logs/traces 可为 real 或 memory。
func New(rules observability.RuleStore, metrics observability.MetricsReader, logs observability.LogsReader, traces observability.TracesReader) *Repo {
	return &Repo{rules: rules, metrics: metrics, logs: logs, traces: traces}
}

// WithEngine 注入告警评估引擎（后台周期评估 + pending/firing/resolved 状态机 + webhook 出站）。
func (r *Repo) WithEngine(e AlertLister) *Repo { r.engine = e; return r }

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

// ListAlerts：引擎注入时读引擎快照（含 pending/firing/resolved 状态，评估由后台循环驱动）；
// 未注入降级即时评估（遍历 rules，对每 enabled 规则调 metrics reader 取匹配 series 当前值评估）。
// real 模式 metrics 来自 Prometheus，memory 模式来自 mock seed。
// targetType/targetId 非空时按维度过滤。
func (r *Repo) ListAlerts(ctx context.Context, targetType, targetId string) ([]observability.Alert, error) {
	if r.engine != nil {
		return r.engine.ListAlerts(ctx, targetType, targetId)
	}
	rules, err := r.rules.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	// 维度下推：targetType/targetId 非空时让 reader 按维度过滤（real 模式少查 Prometheus，
	// 避免全局聚合只为丢掉大部分 series）。读失败降级空继续（告警非关键路径不 5xx）。
	series, err := r.metrics.ListMetrics(ctx, targetType, targetId, "")
	if err != nil {
		log.Printf("observability compose ListAlerts: 取 metrics 失败: %v", err)
		series = nil
	}
	// 下推回退：real 模式 app 维度 targetId 空（TargetID="" 规则 = 全部应用）时 reader 返空
	//（listAppMetrics appID="" 直接返空），但规则本身可评估——回退全租户聚合取 series。
	// 否则 memory seed 的「全部应用」类规则在 real 模式静默失效（R7-I3）。
	if len(series) == 0 {
		if all, aerr := r.metrics.ListMetrics(ctx, "", "", ""); aerr == nil {
			series = all
		}
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
