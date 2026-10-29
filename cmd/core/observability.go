package main

import (
	"context"
	"log"
	"os"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/observability"
	alertengine "github.com/aitoys/paas/internal/observability/alert"
	obcompose "github.com/aitoys/paas/internal/observability/compose"
	obsmemory "github.com/aitoys/paas/internal/observability/memory"
	obspg "github.com/aitoys/paas/internal/observability/pg"
	obreal "github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// buildObservabilityStore 按环境变量构造 observability.Repository：
// alert rules 始终 memory（含 seed）；metrics/logs/traces 按
// PAAS_PROM_URL / PAAS_LOKI_URL / PAAS_JAEGER_URL 非空则接真实后端，
// 否则保持 memory 惰性 mock（三支柱独立、可混用）。
// 未配任何 URL 时行为与现状完全一致。
//
// trace 后端用 Jaeger all-in-one（天生单体，~256Mi 稳定），core 推送端走 OTel OTLP/HTTP
// （PAAS_OTEL_ENDPOINT 指向 Jaeger 4318），后端可插拔（Tempo/Jaeger/Zipkin 均接 OTLP）。
//
// lister 桥接 workload.Repository，供应用级 metrics/logs 按工作负载 pod 名正则查询
// （cAdvisor/Loki 不带 paas 自定义 label，靠 app→工作负载 ID 解析 pod 集合）。nil 时应用级查询降级返空。
// entities 桥接 application/dataservice Repository，供全局（targetType 空）指标聚合
// （可观测大屏「全部」视图健康矩阵）。nil 时全局查询降级返空。
//
// pgStore 是告警状态机/历史事件的 PG 存储（migration 0042）。参数收窄为具体类型：
// 内存路径传入 *obspg.Store(nil)，若参数是接口类型会把 typed nil 装箱成非 nil 接口，
// engine.Restore 放行后 LoadStates 解引用 nil db panic 卡死启动——nil 判断必须在装箱前。
func buildObservabilityStore(lister observability.AppWorkloadLister, entities observability.TenantEntityLister, pgRules observability.RuleStore, pgStore *obspg.Store) (observability.Repository, *alertengine.Engine, diagRawSources) {
	// 告警规则持久化：PG 可用时迁 PG（R4-C1，重启不丢）；否则 memory（含 dev seed）。
	// memory Store 同时实现 RuleStore/MetricsReader/LogsReader/TracesReader（惰性降级），
	// 作为三支柱未接真实后端时的兜底 reader；PG 只实现 RuleStore，兜底 reader 需独立 memory 实例。
	// pgStore 同理：nil 时状态机/历史纯内存。
	fallback := obsmemory.NewStore()
	var rules observability.RuleStore = fallback
	if pgRules != nil {
		rules = pgRules
	}
	// diagRaw 诊断 raw 数据源（spec 2026-10-27 诊断 agent）：real store 的 raw 查询
	// 入口（任意 PromQL/LogQL/Jaeger），未配 URL 的支柱保持 nil（诊断工具返 error 态）。
	var diagRaw diagRawSources
	metrics := observability.MetricsReader(fallback)
	var prober observability.QualityProber // 采集质量探测（spec 专项04 §5）：接真实 Prom 才注入
	if u := os.Getenv("PAAS_PROM_URL"); u != "" {
		promStore := obreal.NewMetricsStore(u, lister, entities)
		metrics = promStore
		prober = promStore
		diagRaw.prom = promStore
	}
	logs := observability.LogsReader(fallback)
	if u := os.Getenv("PAAS_LOKI_URL"); u != "" {
		lokiStore := obreal.NewLogsStore(u, lister)
		logs = lokiStore
		diagRaw.loki = lokiStore
	}
	traces := observability.TracesReader(fallback)
	if u := os.Getenv("PAAS_JAEGER_URL"); u != "" {
		jaegerStore := obreal.NewTracesStore(u, lister, entities)
		traces = jaegerStore
		diagRaw.jaeger = jaegerStore
	}
	repo := obcompose.New(rules, metrics, logs, traces)
	repo.WithQualityProber(prober)
	// 告警评估引擎（R4-C2）：后台 30s tick 评估全部租户规则 + pending/firing/resolved
	// 状态机 + firing webhook 出站。rules 必须支持跨租户列举（memory/pg RuleStore 均实现）。
	var alertStateStore alertengine.StateStore
	var alertEventStore alertengine.EventStore
	if pgStore != nil {
		alertStateStore = pgStore
		alertEventStore = pgStore
	}
	// 通知渠道同源注入：规则引用渠道时按渠道配置投递（启用/静默/重复提醒抑制）；
	// 未注入则保持「只投递规则直连 WebhookURL」的存量行为。
	engine := alertengine.NewEngine(rules.(alertengine.RuleSource), metrics, 0, alertStateStore, alertEventStore).
		WithChannels(rules.(alertengine.ChannelSource))
	repo.WithEngine(engine)
	return repo, engine, diagRaw
}

// diagRawSources 诊断 agent 的 raw 数据源（buildObservabilityStore 附带产出）。
// 三支柱独立可混用；nil 支柱 = 数据源未接入（诊断工具如实返 error 态）。
type diagRawSources struct {
	prom   *obreal.MetricsStore
	loki   *obreal.LogsStore
	jaeger *obreal.TracesStore
}

// workloadLister 桥接 workload.Repository → observability.AppWorkloadLister。
// 应用级可观测按 app→工作负载 ID 列表解析 pod 名正则（Deployment 名=工作负载 ID，Pod=<id>-<hash>-<hash>）。
type workloadLister struct{ repo workload.Repository }

func (l workloadLister) AppWorkloadIDs(ctx context.Context, appID string) ([]string, error) {
	wls, err := l.repo.List(ctx, "", appID, "", "", "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(wls))
	for _, w := range wls {
		if w.ID != "" {
			ids = append(ids, w.ID)
		}
	}
	return ids, nil
}

// AppWorkloadNames 返回应用下全部 service 工作负载名（Deployment/Service 名 = OTel service.name）。
// 应用级 trace 查询按 service.name 匹配工作负载名定位该应用的 span。仅 service 类型（job/cronjob
// 无常驻 HTTP server，不产生入站请求 trace）；同名去重（多环境基线可能同名）。
func (l workloadLister) AppWorkloadNames(ctx context.Context, appID string) ([]string, error) {
	wls, err := l.repo.List(ctx, "", appID, "", "", "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wls))
	seen := make(map[string]struct{}, len(wls))
	for _, w := range wls {
		if w.Type != "service" || w.Name == "" {
			continue
		}
		if _, dup := seen[w.Name]; dup {
			continue
		}
		seen[w.Name] = struct{}{}
		names = append(names, w.Name)
	}
	return names, nil
}

// tenantEntityLister 桥接 application/dataservice Repository → observability.TenantEntityLister。
// 全局指标聚合（可观测大屏「全部」视图）需列出租户全部实体 ID，逐实体查 Prometheus 拼装。
type tenantEntityLister struct {
	apps application.Repository
	ds   dataservice.Repository
	wls  workload.Repository
}

func (l tenantEntityLister) TenantAppIDs(ctx context.Context) ([]string, error) {
	list, err := l.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, a := range list {
		if a.ID != "" {
			ids = append(ids, a.ID)
		}
	}
	return ids, nil
}

func (l tenantEntityLister) TenantDataServiceIDs(ctx context.Context) ([]string, error) {
	list, err := l.ds.List(ctx, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, d := range list {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// TenantServiceNames 列出租户内全部 service 工作负载名（跨应用去重）。
// trace 租户隔离的可见服务白名单：平台级 ListTraces 与 GetTrace 归属校验共用。
func (l tenantEntityLister) TenantServiceNames(ctx context.Context) ([]string, error) {
	list, err := l.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, a := range list {
		wls, err := l.wls.List(ctx, "", a.ID, "", "", "")
		if err != nil {
			continue // 单应用失败降级跳过（与 TenantAppIDs 容错语义一致）
		}
		for _, w := range wls {
			if w.Type != "service" || w.Name == "" {
				continue
			}
			if _, dup := seen[w.Name]; dup {
				continue
			}
			seen[w.Name] = struct{}{}
			names = append(names, w.Name)
		}
	}
	return names, nil
}

// seedDefaultAlertRules 给「无任何告警规则」的租户灌入开箱默认规则
// （observability.DefaultAlertRules，2026-09-25：此前全新部署零规则，告警模块形同虚设）。
// 幂等语义：租户已有规则（含自建/删剩的）不覆盖不重复灌——仅「零规则」租户首次补齐，
// 用户删光后重启会再灌回（无「已 seed」持久标记，权衡后接受：默认集可再删，代价低）。
// demo seed 门控：PAAS_DISABLE_DEMO_SEED=true（生产）跳过，默认告警基线由用户自配。
func seedDefaultAlertRules(ctx context.Context, idRepo identity.Repository, ruleRepo observability.RuleStore) {
	if os.Getenv("PAAS_DISABLE_DEMO_SEED") == "true" {
		return
	}
	if ruleRepo == nil {
		return // 内存路径无持久规则存储（Stores.AlertRules nil）：跳过 seed，observability 内部回退内存规则集
	}
	tenants, err := idRepo.ListTenants(ctx)
	if err != nil {
		log.Printf("[seed] 列租户（默认告警规则）失败: %v", err)
		return
	}
	all, err := ruleRepo.ListAllAlertRules(ctx)
	if err != nil {
		log.Printf("[seed] 列告警规则（默认告警规则）失败: %v", err)
		return
	}
	hasRules := make(map[string]bool, len(all))
	for _, r := range all {
		hasRules[r.TenantID] = true
	}
	for _, t := range tenants {
		if hasRules[t.ID] {
			continue
		}
		tctx := tenant.WithTenant(ctx, t.ID)
		for _, r := range observability.DefaultAlertRules() {
			if _, err := ruleRepo.CreateAlertRule(tctx, r); err != nil {
				log.Printf("[seed] 默认告警规则 %s（%s）失败: %v", r.Name, t.ID, err)
			}
		}
		log.Printf("[seed] 租户 %s 灌入 %d 条默认告警规则", t.ID, len(observability.DefaultAlertRules()))
	}
}

// collectAdminAlerts 只评估请求范围；任一租户失败返回源错误，不能冒充“无告警”。
type adminAlertReader interface {
	ListAllAlertRules(context.Context) ([]observability.AlertRule, error)
	ListAlerts(context.Context, string, string) ([]observability.Alert, error)
}

func collectAdminAlerts(ctx context.Context, repo adminAlertReader, tenantID string) ([]observability.Alert, error) {
	rules, err := repo.ListAllAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	byTenant := map[string]bool{}
	for _, rule := range rules {
		if rule.Enabled && (tenantID == "" || rule.TenantID == tenantID) {
			byTenant[rule.TenantID] = true
		}
	}
	all := make([]observability.Alert, 0)
	for tid := range byTenant {
		alerts, err := repo.ListAlerts(tenant.WithTenant(ctx, tid), "", "")
		if err != nil {
			return nil, err
		}
		for i := range alerts {
			alerts[i].TenantID = tid
		}
		all = append(all, alerts...)
	}
	return all, nil
}
