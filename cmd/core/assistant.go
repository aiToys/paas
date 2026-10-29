// 控制台 AI 助手装配（spec 2026-10-27）：桥接 workload/environment/devops 仓储到
// scoped 窄接口 + service/handler/worker + 路由注册。诊断执行复用 agentRuntime
// （RunScoped 单次工具集注入）与平台诊断同源的数据面 reader。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/apiroute"
	"github.com/aitoys/paas/internal/assistant"
	assistantpg "github.com/aitoys/paas/internal/assistant/pg"
	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/diagnose"
	"github.com/aitoys/paas/internal/diagnose/scoped"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/observability"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"
)

// —— scoped 窄接口桥接（防 scoped 直接依赖领域包）——

type scopedWLReader struct{ repo workload.Repository }

func (r scopedWLReader) Get(ctx context.Context, id string) (scoped.WorkloadInfo, error) {
	w, err := r.repo.Get(ctx, id)
	if err != nil {
		return scoped.WorkloadInfo{}, err
	}
	return scoped.WorkloadInfo{
		ID: w.ID, TenantID: w.TenantID, AppID: w.AppID, EnvID: w.EnvID,
		LaneID: w.LaneID, Service: w.Service, Type: w.Type, Name: w.Name,
	}, nil
}

type scopedEnvNamer struct{ repo environment.Repository }

func (n scopedEnvNamer) EnvName(ctx context.Context, envID string) string {
	e, err := n.repo.Get(ctx, envID)
	if err != nil {
		return "" // 展示用；失败不阻塞（空名如实展示）
	}
	return e.Name
}

type scopedReleaseLister struct{ repo devops.ReleaseRepository }

func (l scopedReleaseLister) RecentByWorkload(ctx context.Context, appID, workloadID string) ([]scoped.ReleaseLite, error) {
	list, err := l.repo.ListReleases(ctx, appID)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	var out []scoped.ReleaseLite
	for _, r := range list {
		if r.WorkloadID != workloadID {
			continue
		}
		out = append(out, scoped.ReleaseLite{
			Version: r.Version, ImageTag: r.ImageDigest, Digest: r.ImageDigest,
			Rollback: r.IsRollback, CreatedAt: r.CreatedAt, By: r.CreatedBy,
		})
		if len(out) >= 5 {
			break
		}
	}
	return out, nil
}

// —— 上下文解析窄接口桥接（spec 2026-10-29 §4/§5）——

// scopedWLLister 本租户工作负载全量（名称匹配候选用；service 过滤在 resolver 内）。
type scopedWLLister struct{ repo workload.Repository }

func (l scopedWLLister) ListWorkloads(ctx context.Context) ([]scoped.WorkloadInfo, error) {
	wls, err := l.repo.List(ctx, "", "", "", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]scoped.WorkloadInfo, 0, len(wls))
	for _, w := range wls {
		out = append(out, scoped.WorkloadInfo{
			ID: w.ID, TenantID: w.TenantID, AppID: w.AppID, EnvID: w.EnvID,
			LaneID: w.LaneID, Service: w.Service, Type: w.Type, Name: w.Name,
		})
	}
	return out, nil
}

// mountAssistant 装配助手模块（handler + worker），返回 mux 挂载函数。
// db nil=内存模式（会话重启丢失，UI 明示）；k8s/prom/loki nil=对应数据源工具降级四态。
// obs 提供 GetTrace（Trace 上下文解析；nil=Trace 锚点降级 unavailable）。
func mountAssistant(
	ctx context.Context,
	stores *Stores,
	db *storagepg.DB,
	runtime *agent.Runtime,
	k8s scoped.K8sReader,
	prom scoped.PromQuerier,
	loki scoped.LokiQuerier,
	budget func(ctx context.Context, tenantID, appID string) error,
	obs observability.Repository,
) http.Handler {
	var repo assistant.Repository
	if db != nil {
		repo = assistantpg.New(db)
		log.Printf("[assistant] 装配 repo=pg（三表持久化）")
	} else {
		repo = assistant.NewMemoryRepo()
		log.Printf("[assistant] 装配 repo=memory（PAAS_DB_URL 未配置，会话重启丢失）")
	}
	resolver := &scoped.TargetResolver{
		Workloads: scopedWLReader{repo: stores.Workload},
		K8s:       k8sSpecReader(k8s),
		Envs:      scopedEnvNamer{repo: stores.Environment},
	}
	svc := assistant.NewService(repo, resolver, budget)
	// 上下文解析器（spec 2026-10-29）：告警/Trace 依赖按运行环境注入，缺项 fail closed
	// 降级为 unavailable（typed-nil 装箱防御：条件赋值，不把 nil 具体类型塞进接口）。
	var alertFinder assistant.AlertEventFinder
	if stores.AlertEvents != nil {
		alertFinder = stores.AlertEvents
	}
	var traceFinder assistant.TraceFinder
	if obs != nil {
		traceFinder = obs // observability.Repository 自带 GetTrace（签名即窄接口）
	}
	svc.SetContextResolver(&assistant.ContextResolver{
		Targets:   resolver,
		Workloads: scopedWLLister{repo: stores.Workload},
		Alerts:    alertFinder,
		Traces:    traceFinder,
	})
	svc.UserFn = gateway.UserIDFrom
	svc.Settings.Persistent = db != nil
	if n, e := strconv.Atoi(os.Getenv("PAAS_ASSISTANT_HISTORY_DAYS")); e == nil && n >= 1 && n <= 365 {
		svc.Settings.HistoryDays = n
	}
	if n, e := strconv.Atoi(os.Getenv("PAAS_ASSISTANT_EVIDENCE_DAYS")); e == nil && n >= 1 && n <= svc.Settings.HistoryDays {
		svc.Settings.EvidenceDays = n
	}

	// 与平台诊断同源默认（PAAS_DIAGNOSE_MODEL 覆盖；空值曾致「底层模型 不存在」全量 exec_failed）。
	diagModel := os.Getenv("PAAS_DIAGNOSE_MODEL")
	if diagModel == "" {
		diagModel = diagnose.DefaultModel
	}
	deps := assistant.Deps{
		Runner: runtime, Model: diagModel,
		Resolver: resolver, K8s: k8s, Prom: prom, Loki: loki,
		Releases: scopedReleaseLister{repo: stores.DevOpsReleases},
	}
	deps.Authorize = func(c context.Context, s assistant.Session) error {
		u, e := stores.Identity.GetUser(c, s.TenantID, s.OwnerUserID)
		if e != nil || u == nil || u.Status == identity.StatusDisabled {
			return fmt.Errorf("user unavailable")
		}
		t, e := stores.Identity.GetTenant(c, s.TenantID)
		if e != nil || t.IsFrozen() {
			return fmt.Errorf("tenant unavailable")
		}
		roles := identity.BuiltinRoles()
		for _, perm := range []identity.Permission{"diagnosis:run", "workload:read", "observability:read"} {
			allowed := false
			for _, name := range u.Roles {
				if roles[name].Grants(perm) {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("permission revoked")
			}
		}
		wl, e := stores.Workload.Get(c, s.Target.WorkloadID)
		if e != nil || wl.AppID != s.Target.AppID {
			return fmt.Errorf("target unavailable")
		}
		app, e := stores.Application.Get(c, wl.AppID)
		if e != nil {
			return fmt.Errorf("application unavailable")
		}
		if app.Restricted {
			admin := false
			for _, role := range u.Roles {
				if role == "tenant-admin" {
					admin = true
				}
			}
			if !admin {
				role, e := stores.AppMembers.MemberRole(c, app.ID, u.ID)
				if e != nil || role == "" {
					return fmt.Errorf("application forbidden")
				}
			}
		}
		return nil
	}
	svc.Authorize = deps.Authorize
	deps.BeforeModel = func(c context.Context, s assistant.Session, reserve int) (func(provider.Usage, string), error) {
		if err := deps.Authorize(c, s); err != nil {
			return nil, err
		}
		// 硬 token 配额先原子预留；金额预算读取真实当期用量，计入本轮保守预估后准入。
		c = gateway.WithApp(c, s.Target.AppID)
		model, e := stores.MaaS.GetModel(c, diagModel)
		if e != nil {
			return nil, fmt.Errorf("model unavailable")
		}
		budgets, e := stores.Billing.ListBudgets(c)
		if e != nil {
			return nil, e
		}
		period := billing.PeriodOf(time.Now())
		rows, e := stores.Billing.ListTokenUsage(c, period)
		if e != nil {
			return nil, e
		}
		price := func(id string) (float64, bool) {
			if id == diagModel {
				return model.OutputPrice, true
			}
			m, e := stores.MaaS.GetModel(c, id)
			return m.OutputPrice, e == nil
		}
		for _, b := range budgets {
			if !b.Enforce || (b.Scope == billing.BudgetScopeApp && b.AppID != s.Target.AppID) {
				continue
			}
			scopedRows := []billing.TokenUsageRow{}
			for _, r := range rows {
				if b.Scope != billing.BudgetScopeApp || r.AppID == b.AppID {
					scopedRows = append(scopedRows, r)
				}
			}
			scopedRows = append(scopedRows, billing.TokenUsageRow{Model: diagModel, Tokens: int64(reserve), AppID: s.Target.AppID})
			state := billing.ComputeBudgetStatus(b, period, scopedRows, price)
			if state.State == billing.BudgetExceeded || len(state.UnpricedModels) > 0 {
				return nil, gateway.ErrBudgetExceeded
			}
		}
		if _, e = stores.Billing.CheckAndInc(c, billing.ResTokens, reserve); e != nil {
			return nil, e
		}
		var once sync.Once
		return func(u provider.Usage, source string) {
			once.Do(func() {
				done, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
				defer cancel()
				actual := u.InputTokens + u.OutputTokens
				if _, err := stores.Billing.IncUsage(done, billing.ResTokens, actual-reserve); err != nil {
					log.Printf("[assistant] quota settlement failed session=%s", s.ID)
				}
				if actual > 0 {
					if err := stores.Billing.AddTokenUsageForTenant(s.TenantID, period, s.Target.AppID, diagModel, int64(actual)); err != nil {
						log.Printf("[assistant] token ledger failed session=%s", s.ID)
					}
				}
			})
		}, nil
	}
	if model, err := stores.MaaS.GetModel(ctx, diagModel); err != nil || len(model.Channels) == 0 {
		svc.SetAgentAvailability(assistant.AgentKeyRuntimeDiagnosis, "诊断模型尚未配置可用通道，请联系平台管理员。 ")
	}
	worker := assistant.NewWorker(svc, repo, deps)
	// 生命周期挂服务 ctx（buildHTTPServer 的 ctx=run 传入的服务 ctx）：此前误用
	// context.Background + 调用方 defer stop——buildHTTPServer 一返回 stop 即触发，
	// worker 出生即死（集群 queued 永不消费的根因）。优雅等待语义经 ctx.Done 桥接。
	stop := worker.Start(ctx, 2)
	go func() {
		<-ctx.Done()
		stop()
	}()

	svc.Audit = func(c context.Context, action, id string) {
		tid, _ := tenant.TenantFrom(c)
		actor := assistant.OwnerFrom(c)
		_ = (&identityAuditAdapter{store: stores.Security}).Record(c, tid, actor, action, "assistant", id, "")
	}
	h := assistant.NewHandler(svc, gateway.RequestAllowed)
	return h
}

// k8sSpecReader 从 K8sReader 收窄出 SpecReader（UID 快照用；nil 透传 nil）。
func k8sSpecReader(k8s scoped.K8sReader) scoped.SpecReader {
	if k8s == nil {
		return nil
	}
	return k8s
}

// registerAssistantRoutes OpenAPI 登记 + mux 挂载。
func registerAssistantRoutes(mux *http.ServeMux, reg *apiroute.Registry, h http.Handler, auth func(http.Handler) http.Handler) {
	reg.Operation("GET", "/api/assistant/sessions/{id}/messages", apiroute.Tags("AI 助手"), apiroute.Summary("恢复连续对话消息"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp(assistant.Conversation{}))
	reg.Operation("GET", "/api/assistant/settings", apiroute.Tags("AI 助手"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp(assistant.Settings{}))
	mux.Handle("/api/assistant/", auth(gateway.Require(assistant.PermDiagnosisRead)(h)))
	reg.Operation("GET", "/api/assistant/agents", apiroute.Tags("AI 助手"), apiroute.Summary("官方 Agent 目录（服务端静态注册）"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp([]assistant.AgentDesc{}))
	reg.Operation("POST", "/api/assistant/sessions", apiroute.Tags("AI 助手"), apiroute.Summary("创建诊断会话（目标服务端授权解析，拒客户端作用域参数）"), apiroute.Perm(assistant.PermDiagnosisRun), apiroute.WithReqBody(assistant.CreateSessionRequest{}), apiroute.WithResp(assistant.Session{}))
	reg.Operation("GET", "/api/assistant/sessions", apiroute.Tags("AI 助手"), apiroute.Summary("当前用户会话列表"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp([]assistant.Session{}))
	reg.Operation("GET", "/api/assistant/sessions/{id}", apiroute.Tags("AI 助手"), apiroute.Summary("会话详情"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp(assistant.Session{}))
	reg.Operation("GET", "/api/assistant/sessions/{id}/runs", apiroute.Tags("AI 助手"), apiroute.Summary("会话任务列表（诊断历史）"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp([]assistant.RunView{}))
	reg.Operation("POST", "/api/assistant/sessions/{id}/runs", apiroute.Tags("AI 助手"), apiroute.Summary("发起诊断任务（202 受理；Idempotency-Key 幂等）"), apiroute.Perm(assistant.PermDiagnosisRun), apiroute.WithHeader("Idempotency-Key", "同一会话内的请求幂等键", true), apiroute.WithReqBody(assistant.CreateRunRequest{}), apiroute.WithResp(assistant.RunView{}))
	reg.Operation("GET", "/api/assistant/runs/{id}", apiroute.Tags("AI 助手"), apiroute.Summary("任务详情（轮询终态）"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp(assistant.RunView{}))
	reg.Operation("GET", "/api/assistant/runs/{id}/events", apiroute.Tags("AI 助手"), apiroute.Summary("任务进度事件（afterSeq 增量）"), apiroute.Perm(assistant.PermDiagnosisRead), apiroute.WithResp([]assistant.Event{}))
	reg.Operation("POST", "/api/assistant/runs/{id}/cancel", apiroute.Tags("AI 助手"), apiroute.Summary("取消任务（终态幂等）"), apiroute.Perm(assistant.PermDiagnosisRun), apiroute.WithResp(assistant.RunView{}))
	reg.Operation("POST", "/api/assistant/sessions/{id}/context/resolve", apiroute.Tags("AI 助手"), apiroute.Summary("确认诊断上下文候选（candidateId 服务端生成；伪造 404+审计）"), apiroute.Perm(assistant.PermDiagnosisRun), apiroute.WithReqBody(struct {
		CandidateID string `json:"candidateId"`
	}{}), apiroute.WithResp(assistant.Session{}))
}
