// Package alert 提供告警评估引擎（可观测 10 轮审查 R4-C2）：
// 后台 goroutine 周期评估全部租户的告警规则，维护 pending→firing→resolved 状态机，
// firing 转变时 POST webhook 出站。解决「即时评估模型——无人看页面告警不评估、
// webhook 永不触发」的架构缺陷。
//
// 引擎是告警状态唯一真源：ListAlerts 返回引擎快照（含状态/首末触发时间），
// handler 不再即时评估。引擎未启动（nil）时调用方降级即时评估（compose 保持原路径）。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// 评估间隔与 pending 持续窗口。pending→firing 需连续 breach（2 个 tick），防瞬时毛刺。
const (
	DefaultInterval   = 30 * time.Second
	breachToFireTicks = 2
	maxAlertsPerRule  = 50 // 单规则并发告警上界（TargetID 空 = 全部 target，防爆量）
)

// Engine 周期评估告警规则并维护告警状态机。
type Engine struct {
	rules    RuleSource
	metrics  observability.MetricsReader
	client   *http.Client
	interval time.Duration
	states2  StateStore // 可选：状态机持久化（nil=纯内存）
	events   EventStore // 可选：历史事件落库（nil=不落）

	mu     sync.RWMutex
	states map[string]*alertState // key = ruleID|targetType|targetID

	stop chan struct{}
	done chan struct{}
}

// RuleSource 规则来源（跨租户全量；memory/pg RuleStore 均实现）。
type RuleSource interface {
	ListAllAlertRules(ctx context.Context) ([]observability.AlertRule, error)
}

// PersistedState 引擎状态机持久化快照（重启恢复用）。
type PersistedState struct {
	StateKey   string
	TenantID   string
	Alert      observability.Alert
	TickBreach int
}

// StateStore 告警状态机持久化（PG 实现；nil 降级纯内存）。
// 引擎是跨租户组件（评估全租户规则），Save/Delete 不走 ctx 租户过滤，
// 数据自带 tenant_id；Load 由 owner 连接调用（RLS 放行）。
type StateStore interface {
	LoadStates(ctx context.Context) ([]PersistedState, error)
	SaveStates(ctx context.Context, states []PersistedState) error
	DeleteStates(ctx context.Context, keys []string) error
}

// EventStore 告警历史事件持久化（只增不删 + 租户级保留上限，PG 实现；nil 不落历史）。
type EventStore interface {
	AppendEvent(ctx context.Context, ev observability.AlertEvent) error
}

// alertState 单条告警的状态机（内存态为主；StateStore 注入时变更批次落 PG，重启恢复）。
type alertState struct {
	alert      observability.Alert
	tickBreach int // 连续命中计数（达 breachToFireTicks 转 firing）
}

// NewEngine 创建评估引擎。interval<=0 用 DefaultInterval。
// states2/events 可选持久化注入（PG 路径）；nil 时纯内存行为与历史版本一致。
func NewEngine(rules RuleSource, metrics observability.MetricsReader, interval time.Duration, states2 StateStore, events EventStore) *Engine {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Engine{
		rules:    rules,
		metrics:  metrics,
		client:   httputil.NewClient(10 * time.Second),
		interval: interval,
		states2:  states2,
		events:   events,
		states:   map[string]*alertState{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Restore 从 StateStore 恢复状态机（Start 前调用一次；重启不重置 pending/firing 计数）。
func (e *Engine) Restore(ctx context.Context) {
	if e.states2 == nil {
		return
	}
	loaded, err := e.states2.LoadStates(ctx)
	if err != nil {
		log.Printf("alert engine: 恢复告警状态失败（从空状态开始）: %v", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ps := range loaded {
		e.states[ps.StateKey] = &alertState{alert: ps.Alert, tickBreach: ps.TickBreach}
	}
	if len(loaded) > 0 {
		log.Printf("alert engine: 恢复 %d 条告警状态", len(loaded))
	}
}

// Start 启动后台评估循环（先跑一轮立即出结果，再按 interval tick）。
// ctx 取消或 Stop 均退出。
func (e *Engine) Start(ctx context.Context) {
	go func() {
		defer close(e.done)
		e.evaluate(ctx) // 启动先跑一轮（部署后无需等 30s 即有告警状态）
		t := time.NewTicker(e.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stop:
				return
			case <-t.C:
				e.evaluate(ctx)
			}
		}
	}()
}

// Stop 停止评估循环（幂等；等待 goroutine 退出）。
func (e *Engine) Stop() {
	select {
	case <-e.stop:
		// 已关闭
	default:
		close(e.stop)
	}
	<-e.done
}

// evaluate 单轮评估：跨租户取规则，按租户分组评估（metrics reader 需租户 ctx 过滤）。
func (e *Engine) evaluate(ctx context.Context) {
	rules, err := e.rules.ListAllAlertRules(context.WithoutCancel(ctx))
	if err != nil {
		log.Printf("alert engine: 取告警规则失败: %v", err)
		return
	}
	// 按租户分组（ListMetrics 按 ctx tenant 过滤）。
	byTenant := map[string][]observability.AlertRule{}
	for _, r := range rules {
		byTenant[r.TenantID] = append(byTenant[r.TenantID], r)
	}
	seen := map[string]struct{}{} // 本轮命中的 state key（未命中的转 resolved）
	now := time.Now()
	var webhooks []observability.AlertRule
	var fired []observability.Alert

	for tid, trs := range byTenant {
		tctx := tenant.WithTenant(context.WithoutCancel(ctx), tid)
		series, err := e.metrics.ListMetrics(tctx, "", "", "")
		if err != nil {
			log.Printf("alert engine: 取 metrics 失败 tenant=%s: %v", tid, err)
			continue
		}
		for _, rule := range trs {
			if !rule.Enabled {
				continue
			}
			count := 0
			for _, s := range series {
				if !rule.Matches(s) {
					continue
				}
				if !rule.Breached(s.Current) {
					continue
				}
				if count++; count > maxAlertsPerRule {
					break
				}
				key := rule.ID + "|" + s.TargetType + "|" + s.TargetID
				seen[key] = struct{}{}
				st := e.getState(key)
				st.tickBreach++
				if st.alert.Status == observability.AlertFiring {
					// 已 firing：更新观测值与时间，状态不变。
					st.alert.Value = s.Current
					st.alert.LastSeen = now
					continue
				}
				if st.tickBreach >= breachToFireTicks {
					// pending → firing：正式告警 + 出站。
					st.alert = observability.Alert{
						RuleID: rule.ID, RuleName: rule.Name, TenantID: tid,
						TargetType: s.TargetType, TargetID: s.TargetID,
						MetricName: s.Name, Value: s.Current,
						Threshold: rule.Threshold, Operator: rule.Operator,
						Severity: rule.Severity, Status: observability.AlertFiring,
						FiredAt: now, LastSeen: now,
					}
					fired = append(fired, st.alert)
					if rule.WebhookURL != "" {
						webhooks = append(webhooks, rule)
					}
				} else if st.alert.Status == "" {
					// 首次命中：pending。
					st.alert = observability.Alert{
						RuleID: rule.ID, RuleName: rule.Name, TenantID: tid,
						TargetType: s.TargetType, TargetID: s.TargetID,
						MetricName: s.Name, Value: s.Current,
						Threshold: rule.Threshold, Operator: rule.Operator,
						Severity: rule.Severity, Status: observability.AlertPending,
						FiredAt: now, LastSeen: now,
					}
				} else {
					st.alert.Value = s.Current
					st.alert.LastSeen = now
				}
			}
		}
	}
	// 未命中且曾命中（pending/firing）→ resolved；已 resolved 的保留一轮后清理。
	e.mu.Lock()
	var expired []string
	var resolved []observability.Alert
	for key, st := range e.states {
		if _, ok := seen[key]; ok {
			continue
		}
		switch st.alert.Status {
		case observability.AlertPending, observability.AlertFiring:
			st.alert.Status = observability.AlertResolved
			st.alert.LastSeen = now
			st.tickBreach = 0
			resolved = append(resolved, st.alert)
		case observability.AlertResolved:
			expired = append(expired, key) // resolved 展示一轮后清理（防列表无限膨胀）
		}
	}
	for _, key := range expired {
		delete(e.states, key)
	}
	e.mu.Unlock()

	// webhook 出站（锁外，防慢 webhook 阻塞评估循环）：每条 firing 通知一次。
	for i, rule := range webhooks {
		e.postWebhook(fired[i], rule.WebhookURL)
	}

	// 持久化（锁外，WithoutCancel 防 SIGTERM 后用已 cancel ctx 落库）：
	// firing/resolved 转变写历史事件；本轮有状态的条目 upsert、过期条目删除。
	if e.states2 != nil || e.events != nil {
		pctx := context.WithoutCancel(ctx)
		if e.events != nil {
			for _, a := range fired {
				if err := e.events.AppendEvent(pctx, observability.AlertEventFromAlert(a, observability.AlertFiring, now)); err != nil {
					log.Printf("alert engine: 落告警历史失败: %v", err)
				}
			}
			for _, a := range resolved {
				if err := e.events.AppendEvent(pctx, observability.AlertEventFromAlert(a, observability.AlertResolved, now)); err != nil {
					log.Printf("alert engine: 落告警历史失败: %v", err)
				}
			}
		}
		if e.states2 != nil {
			e.persistStates(pctx, seen, expired, now)
		}
	}
}

// persistStates 把本轮命中的全部状态 + resolved 未过期的条目批次落库，过期条目删除。
// 只在本轮有变更（seen/expired 非空）时写，避免每 tick 全量 upsert。
func (e *Engine) persistStates(ctx context.Context, seen map[string]struct{}, expired []string, now time.Time) {
	if len(seen) == 0 && len(expired) == 0 {
		return
	}
	e.mu.RLock()
	batch := make([]PersistedState, 0, len(seen))
	for key := range seen {
		if st, ok := e.states[key]; ok && st.alert.Status != "" {
			batch = append(batch, PersistedState{
				StateKey: key, TenantID: st.alert.TenantID, Alert: st.alert, TickBreach: st.tickBreach,
			})
		}
	}
	e.mu.RUnlock()
	if len(batch) > 0 {
		if err := e.states2.SaveStates(ctx, batch); err != nil {
			log.Printf("alert engine: 落告警状态失败: %v", err)
		}
	}
	if len(expired) > 0 {
		if err := e.states2.DeleteStates(ctx, expired); err != nil {
			log.Printf("alert engine: 清理过期告警状态失败: %v", err)
		}
	}
}

// getState 取或建状态（调用方持锁或单 goroutine evaluate 内使用）。
func (e *Engine) getState(key string) *alertState {
	if st, ok := e.states[key]; ok {
		return st
	}
	st := &alertState{}
	e.states[key] = st
	return st
}

// postWebhook POST 告警 JSON 到 rule.WebhookURL（fire-and-forget，失败记日志不重试）。
func (e *Engine) postWebhook(a observability.Alert, url string) {
	body, _ := json.Marshal(map[string]any{
		"ruleId": a.RuleID, "ruleName": a.RuleName, "status": a.Status,
		"targetType": a.TargetType, "targetId": a.TargetID,
		"metricName": a.MetricName, "value": a.Value, "threshold": a.Threshold,
		"operator": a.Operator, "severity": a.Severity, "firedAt": a.FiredAt,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("alert engine: 构造 webhook 请求失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		log.Printf("alert engine: webhook 出站失败 url=%s: %v", url, err)
		return
	}
	resp.Body.Close()
}

// ListAlerts 返回引擎告警快照（按租户过滤；firing 优先、severity critical 优先、时间倒序）。
// targetType/targetId 非空时按维度过滤。pending/resolved 一并返回（前端状态可见）。
func (e *Engine) ListAlerts(ctx context.Context, targetType, targetId string) ([]observability.Alert, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]observability.Alert, 0, len(e.states))
	for _, st := range e.states {
		a := st.alert
		if a.TenantID != tid {
			continue
		}
		if targetType != "" && a.TargetType != targetType {
			continue
		}
		if targetId != "" && a.TargetID != targetId {
			continue
		}
		out = append(out, a)
	}
	rank := map[string]int{observability.AlertFiring: 0, observability.AlertPending: 1, observability.AlertResolved: 2}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == observability.SeverityCritical
		}
		return out[i].FiredAt.After(out[j].FiredAt)
	})
	return out, nil
}
