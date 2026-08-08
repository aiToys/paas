// Package main 是 Platform Core 的启动入口。
// 职责：组装依赖、注册插件、按拓扑顺序 Init+Run 插件、暴露 Gateway 与探针端点。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/eval"
	"github.com/aitoys/paas/internal/ai/guardrail"
	"github.com/aitoys/paas/internal/apiroute"
	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/backup"
	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/core/application"
	authPkg "github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/core/health"
	"github.com/aitoys/paas/internal/core/identity"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/internal/dashboard"
	"github.com/aitoys/paas/internal/dataplane"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/devops/registry"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/internal/messaging"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/internal/observability/tracing"
	"github.com/aitoys/paas/internal/security"
	"github.com/aitoys/paas/internal/web"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/plugin"
	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	gw := gateway.New()
	meter := &gateway.Meter{}

	if err := run(ctx, gw, meter); err != nil {
		log.Fatalf("core 启动失败: %v", err)
	}
}

// run 启动 HTTP 服务并引导插件；返回非 nil 则进程以 1 退出。
func run(ctx context.Context, gw *gateway.Gateway, meter *gateway.Meter) error {
	// OTel tracer 初始化（PAAS_OTEL_ENDPOINT 非空接 OTLP/HTTP 后端，空=noop 行为不变）。
	tracerShutdown, err := tracing.Init(ctx, os.Getenv("PAAS_OTEL_ENDPOINT"))
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(shutdownCtx); err != nil {
			log.Printf("[otel] tracer shutdown: %v", err)
		}
	}()
	// 启动 K8s 数据面（PAAS_KUBECONFIG 非空），返回 applier 注入 workload/dataservice repo（空则保持现状）。
	appliers, managerCancel := startManager()
	if managerCancel != nil {
		defer managerCancel()
	}
	// 选择持久化后端并 seed（PAAS_DB_URL 非空走 PG，否则内存）。
	stores, closeDB, err := buildAllStores(ctx, appliers)
	if err != nil {
		return err
	}
	if closeDB != nil {
		defer closeDB() // 进程退出释放连接池
	}
	// 延迟注入 AppConfigLookup 到 WorkloadReconciler（startManager 先于 stores 构造）。
	// 让"绑定资源"（数据服务连接 + 模型 LLM 凭证）经 reconciler 真正注入 Pod env 生效。
	// 启动初期无 CRD 事件，赋值与首条 reconcile 间无实际并发（best-effort + 单 worker）。
	if appliers.wlReconciler != nil {
		appliers.wlReconciler.Configs = appConfigLookup{repo: stores.AppConfig}
	}
	// CredentialResolver 桥接 security store：注入 MaaS 插件解析平台级凭证。
	// security.SecretStore 接口含 Resolve（PG/memory 透明）。
	deps := realCoreDeps{gw: gw, resolver: secretResolver{store: stores.Security.(security.SecretStore)}}
	// MaaS 插件注入已 seed 的模型仓储（Init 从 store 加载模型 + BuildProvider 重建通道，注册 gateway）。
	plugins := []plugin.Plugin{maas.NewMaaSPlugin(stores.MaaS)}
	// 注入进程级 ctx 给 devops store：构建 goroutine 感知 shutdown（runBuild 派生 baseCtx，
	// K8sJob 子 ctx随之 cancel），避免 SIGTERM 后 in-flight 构建永久卡 running。
	if ds, ok := stores.DevOpsBuilds.(interface{ SetBaseCtx(context.Context) }); ok {
		ds.SetBaseCtx(ctx)
	}
	// 崩溃恢复：把上次进程重启中断的构建（pending/running）标记 failed，防永久卡死
	// （正常 SIGTERM 有 baseCtx cancel 兜底，kill -9/Pod 强删不覆盖，启动 sweep 兜底）。
	if ds, ok := stores.DevOpsBuilds.(interface {
		SweepInterrupted(context.Context) error
	}); ok {
		if err := ds.SweepInterrupted(ctx); err != nil {
			log.Printf("[devops] 崩溃恢复 sweep 失败（继续启动）: %v", err)
		}
	}
	srv := serveHTTP(gw, meter, stores, appliers)
	ran, err := bootstrapCore(ctx, plugins, deps)
	if err != nil {
		return err
	}
	log.Printf("core 启动完成，已运行插件: %v", ran)
	<-ctx.Done()
	// 优雅关闭：HTTP Shutdown 给 in-flight 请求（含 SSE 流式）30s grace 期，避免半写/连接强制断。
	log.Println("core 收到退出信号，优雅关闭中（最多等 30s）...")
	shutdownCtx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer scancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

// bootstrapCore 把插件注册到 Registry，按拓扑顺序 Init + Run。
// 返回 map[插件名]是否成功运行。任一插件 Init/Run 失败则中止并返回错误。
func bootstrapCore(ctx context.Context, plugins []plugin.Plugin, deps plugin.CoreDeps) (map[string]bool, error) {
	registry := coreplugin.NewRegistry()
	for _, p := range plugins {
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	ordered, err := registry.LoadOrder()
	if err != nil {
		return nil, fmt.Errorf("插件加载顺序解析失败: %w", err)
	}

	ran := map[string]bool{}
	for _, p := range ordered {
		if err := p.Init(ctx, deps); err != nil {
			return ran, fmt.Errorf("插件 %s 初始化失败: %w", p.Manifest().Name, err)
		}
		if err := p.Run(ctx); err != nil {
			return ran, fmt.Errorf("插件 %s 运行失败: %w", p.Manifest().Name, err)
		}
		ran[p.Manifest().Name] = true
	}
	return ran, nil
}

// realCoreDeps 是 CoreDeps 的真实实现，向插件注入 Gateway。
type realCoreDeps struct {
	gw       *gateway.Gateway
	resolver provider.CredentialResolver // T5 接线：security store 桥接的凭证解析器
}

func (d realCoreDeps) Logger() interface{}                { return nil }
func (d realCoreDeps) Gateway() provider.GatewayRegistrar { return d.gw }
func (d realCoreDeps) SecretResolver() provider.CredentialResolver {
	return d.resolver // 可为 nil（未配置第三方供应商时，真实通道返回 ErrCredentialMissing）
}

// secretResolver 适配 security store → provider.CredentialResolver（依赖倒置）。
// 仅解析平台级 Secret 明文（store.Resolve 内部约束），供第三方供应商通道运行时取 API Key。
// 持有 security.SecretStore 接口（含 Resolve），PG/memory 后端透明。
type secretResolver struct{ store security.SecretStore }

func (r secretResolver) Resolve(ref string) (string, error) {
	sec, err := r.store.Resolve(context.Background(), ref)
	if err != nil {
		return "", err
	}
	return sec.Value, nil
}

// dsEnvLookup 适配 dataservice.Repository → backup.ResourceEnvResolver：
// 按资源 ID 取所属数据服务的环境 ID（租户隔离由 dataservice.Get 保证）。
type dsEnvLookup struct{ repo dataservice.Repository }

func (d dsEnvLookup) ResourceEnv(ctx context.Context, id string) (string, error) {
	ds, err := d.repo.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return ds.EnvID, nil
}

// dsInstanceReader 桥接 dataplane.EndpointsReader -> dataservice.InstanceReader（admin 详情读实例）。
// 复用 /dp/ 的 Endpoints 读取真源；admin 以资源所属租户 ctx 调用穿透 reader 的 tenant label 校验。
type dsInstanceReader struct{ r dataplane.EndpointsReader }

func (a dsInstanceReader) Instances(ctx context.Context, ns, svc string) ([]dataservice.InstanceInfo, error) {
	if a.r == nil {
		return nil, nil
	}
	list, err := a.r.Instances(ctx, ns, svc)
	if err != nil {
		return nil, err
	}
	out := make([]dataservice.InstanceInfo, 0, len(list))
	for _, x := range list {
		out = append(out, dataservice.InstanceInfo{Name: x.Name, IP: x.IP, Port: x.Port})
	}
	return out, nil
}

// tenantChecker 桥接 identity.Repository -> dataservice/environment TenantChecker（代建校验租户存在）。
type tenantChecker struct{ repo identity.Repository }

func (c tenantChecker) Exists(ctx context.Context, tenantID string) error {
	t, err := c.repo.GetTenant(ctx, tenantID)
	if err != nil || t.ID == "" {
		return fmt.Errorf("租户不存在: %s", tenantID)
	}
	return nil
}

// quotaCheckFn 桥接 billing.CheckAndInc -> dataservice.QuotaCheckFunc（dataservices 维度）。
// ctx 必须带目标租户（admin 代建时已 WithTenant）。
func quotaCheckFn(bill billing.Repository) dataservice.QuotaCheckFunc {
	return func(ctx context.Context, delta int) error {
		if _, err := bill.CheckAndInc(ctx, billing.ResDataservices, delta); err != nil {
			return err
		}
		return nil
	}
}

// resolveAPIKey 解析 API Key：环境变量 PAAS_API_KEY 优先，否则用开发默认值（sk-acme-admin）。
// 返回值用于 seed 兼容（自定义 Key 追加为 t-acme admin）与日志展示。
func resolveAPIKey() string {
	if k := os.Getenv("PAAS_API_KEY"); k != "" {
		return k
	}
	return "sk-acme-admin"
}

// resolveJWTSecretOrErr 返回 JWT secret 与可能的配置错误（可测，不调 os.Exit）。
//   - PAAS_JWT_SECRET 非空 -> 用之
//   - 空 + 生产模式（PAAS_PROD=true）-> 报错拒启
//   - 空 + dev 模式（PAAS_PROD 未设）-> 随机生成 32 字节（保持本地零配置启动，重启后旧 token 失效）
//
// 偏离 plan 的 PAAS_DEV：改用正向 PAAS_PROD 标识生产，本地 ./bin/core 不设 env 仍可随机启动（不破坏现状）。
func resolveJWTSecretOrErr() (string, error) {
	if s := os.Getenv("PAAS_JWT_SECRET"); s != "" {
		// 生产强制 ≥32 字节：防运维误配弱串（"paas" 等）被 hashcat 暴破伪造 token。
		if os.Getenv("PAAS_PROD") == "true" && len(s) < 32 {
			return "", fmt.Errorf("PAAS_JWT_SECRET 过短：生产环境（PAAS_PROD=true）要求 ≥32 字节（当前 %d）", len(s))
		}
		return s, nil
	}
	if os.Getenv("PAAS_PROD") == "true" {
		return "", fmt.Errorf("PAAS_JWT_SECRET 未配置：生产环境（PAAS_PROD=true）必须显式设置（≥32 字节随机串）")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 JWT secret 失败: %w", err)
	}
	log.Printf("[auth] PAAS_JWT_SECRET 未配置，dev 模式随机生成（生产 PAAS_PROD=true 请配置）")
	return base64.StdEncoding.EncodeToString(b), nil
}

// resolveJWTSecret 启动路径用：Fatal 包装 resolveJWTSecretOrErr。
func resolveJWTSecret() string {
	s, err := resolveJWTSecretOrErr()
	if err != nil {
		log.Fatalf("[auth] %v", err)
	}
	return s
}

// serveHTTP 挂载 OpenAI 兼容端点（鉴权 + RBAC）与存活探针。
//
// 所有 store 由 run() 经 buildAllStores 构造（PG 或内存两路径统一形态）注入，
// handler 对后端透明（PG/memory 同接口）。横切注入：
//   - QuotaCheck：application/workload Create 前调 billing.CheckAndInc，超限回 429。
//     CheckAndInc 是接口方法，PG/memory 多态透明。
//   - EnvTypeResolver：注入 environment store（实现 EnvType 方法），prod:write 校验跨模块复用。
//
// 模型目录平台共享（不按租户过滤）；应用/治理/配置等业务模块按租户隔离。
func serveHTTP(gw *gateway.Gateway, meter *gateway.Meter, stores *Stores, appliers k8sAppliers) *http.Server {
	apiKey := resolveAPIKey()
	// P3-2 计量采集：推理 token 用量回写 billing（meter.OnTokens 钩子）。
	// appID 来自应用级 Key（强制归因到应用）；user 是 agent 软标签（仅日志，不入 billing 聚合）。
	meter.OnTokens = func(tenantID, appID, user string, tokens int) {
		if tenantID != "" {
			ctx := tenant.WithTenant(context.Background(), tenantID)
			ctx = gateway.WithApp(ctx, appID)
			_, _ = stores.Billing.IncUsage(ctx, billing.ResTokens, tokens)
		}
	}
	jwtSecret := resolveJWTSecret()
	mux := http.NewServeMux()
	// BearerAuth 双通道：JWT（admin 浏览器登录）/ API Key（程序化调用）共存，下游零改动。
	auth := gateway.BearerAuth(stores.Identity, jwtSecret)
	// 生产强制 Secure cookie：PAAS_PROD=true 且未显式开 PAAS_COOKIE_SECURE=true 时拒启，
	// 防生产误用 HTTP 致 access(15min)+refresh(7d) cookie 被嗅探（应配 TLS 后 cookieSecure=true）。
	cookieSecure := os.Getenv("PAAS_COOKIE_SECURE") == "true"
	if os.Getenv("PAAS_PROD") == "true" && !cookieSecure {
		log.Fatalf("[auth] PAAS_PROD=true 时必须开启 PAAS_COOKIE_SECURE=true（配 TLS 后启用），防 cookie 嗅探")
	}
	authHandler := authPkg.NewHandler(stores.Identity, jwtSecret, cookieSecure)
	// 注入审计记录器：登录/登出/失败记 security.AuditLog（adapter 桥接 + 注入 ctx tenant）。
	authHandler = authHandler.WithAudit(&authAuditAdapter{store: stores.Security})
	// identity 管理 API（/api/admin/tenants、/api/admin/users、/api/admin/api-keys、/api/admin/roles）：平台运维域，需 super_admin。
	idmHandler := identity.NewHandler(stores.Identity).
		HashPassword(authPkg.HashPassword).
		PasswordValidator(authPkg.ValidatePassword)
	// 注入平台超管判定：IsAdmin 用户 token 携带 super_admin 标记（auth.issueTokens），
	// 据此区分跨租户平台管理与本租户 tenant-admin，防越权。
	idmHandler.IsPlatformAdmin = gateway.IsPlatformAdmin
	// 注入调用者用户 ID/角色（桥接 gateway ctx）：自助 API Key 端点据此绑定密钥归属 +
	// roles 封顶（只能赋予自身持有角色，零提权）。
	idmHandler.CallerUserID = func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }
	idmHandler.CallerRoles = func(r *http.Request) []string {
		rs, _ := gateway.RolesFrom(r.Context())
		return rs
	}
	// 注入审计记录器：identity 写操作（签发/吊销密钥、增删用户/租户）记 security.AuditLog
	// （adapter 桥接 + 注入 ctx tenant），与 auth 登录审计同源，满足合规「审计只增不删」。
	idmHandler = idmHandler.WithAudit(&identityAuditAdapter{store: stores.Security})
	adminGuard := func(h http.Handler) http.Handler {
		// 平台级管理（租户/用户/API Key/dashboard 跨租户聚合）需 super_admin 角色，
		// 防 tenant-admin 越权枚举全部租户与用户。auth 先解析身份注入 roles，再校验超管。
		return auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !gateway.IsPlatformAdmin(r) {
				httputil.WriteError(w, http.StatusForbidden, "需要平台超管权限")
				return
			}
			h.ServeHTTP(w, r)
		}))
	}
	// dashboard 聚合（console-admin 首页统计）。
	dashHandler := dashboard.NewHandler(stores.Identity)
	// messaging（MQ topic/消费组）：方法级权限 dataservice:read/write（MQ 属数据服务）。
	msgHandler := messaging.NewHandler(stores.Messaging)
	msgHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// backup（数据服务备份）：方法级权限 dataservice:read/write + 生产数据服务备份需 prod:write。
	bkHandler := backup.NewHandler(stores.Backup,
		backup.WithEnvResolver(stores.Environment, dsEnvLookup{repo: stores.DataService}))
	bkHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// apiroute Registry：路由 + OpenAPI 元数据单一真源，own 同一个 mux。
	// Register 同时驱动 mux（Go 1.22 method-scoped）与 spec；Operation 仅记 spec（composite 子操作）。
	reg := apiroute.New(mux, apiroute.Info{
		Title:       "PaaS Platform API",
		Version:     "1.0",
		Description: "一站式 PaaS 平台——服务治理 / 中间件 / MaaS / DevOps 统一控制面",
	})
	// agentDispatcherHolder：agent:{id} 虚拟模型路由的 late-binding 持有者。
	// 此处注册进 /v1/chat/completions，agentRuntime 构造后（下方 AI 装配段）Set 注入。
	agentDispatcherHolder := &gateway.AgentDispatcherHolder{}
	mux.Handle("/v1/chat/completions", auth(gateway.Require("model:infer")(gateway.ChatCompletions(gw, meter, agentDispatcherHolder))))
	// 模型目录端点（GET-only）走 Register：验证 mux 驱动 + spec 生成双路径。
	reg.Register("GET", "/v1/models", auth(gateway.Require("model:read")(gateway.ListModels(gw))),
		apiroute.Tags("MaaS"), apiroute.Summary("OpenAI 兼容模型列表"),
		apiroute.Perm("model:read"), apiroute.WithResp(nil))
	reg.Register("GET", "/api/models", auth(gateway.Require("model:read")(gateway.CatalogModels(gw))),
		apiroute.Tags("MaaS"), apiroute.Summary("模型市场富信息（含通道）"),
		apiroute.Perm("model:read"), apiroute.WithResp(nil))
	// 共享 K8s 状态读取器：workload handler（List 回填真实 Ready/Status）+ 应用 handler
	// （派生 Replicas/Status）复用同一实例。clientset nil（非集群部署）时 no-op 降级透传 store 原值。
	statusReader := controller.NewK8sStatusReader(appliers.clientset)
	// 应用为主线 REST API：方法级权限由 handler 内部按 application:*/binding:write 校验
	// 横切配额拦截：创建应用前调 billing.CheckAndInc，超限回 429（stores.Billing 共享用量真源）。
	// WithWorkloadStats 派生应用 Replicas/Status（从真实工作负载聚合，覆盖 seed 假值）。
	appHandler := application.NewHandler(stores.Application,
		application.WithWorkloadStats(&appWorkloadStats{wlRepo: stores.Workload, status: statusReader}))
	appHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	appHandler.QuotaCheck = func(ctx context.Context, delta int) error {
		_, err := stores.Billing.CheckAndInc(ctx, billing.ResApplications, delta)
		return err
	}
	// workload 维度配额回收（级联删工作负载 + admin 删除共用，DRY 单一真源）。
	wlQuotaFn := func(ctx context.Context, delta int) error {
		_, err := stores.Billing.CheckAndInc(ctx, billing.ResWorkloads, delta)
		return err
	}
	// 绑定数据服务时自动注入连接信息到 appconfig（DATABASE_URL/REDIS_URL/...），工作负载重启即生效。
	appHandler.Binder = &dsBindingInjector{dsRepo: stores.DataService, cfgRepo: stores.AppConfig, appRepo: stores.Application, idb: stores.Identity, kbRepo: stores.KnowledgeBase}
	// 删除应用时级联清理关联资源（best-effort）：工作负载（含 K8s Deployment/Job）+ 应用配置（env/Secret）。
	// 工作负载删除成功后回收 workload 维度配额（与 workload handler Delete 对齐）。
	// devops 历史记录（仓库/构建/镜像/发布）保留作历史归档，不随应用删除。
	// admin 删应用复用同一 appCascadeDeleter（DRY，注入 wlQuota）。
	appHandler.CascadeDelete = appCascadeDeleter{
		wl:      stores.Workload,
		cfg:     stores.AppConfig,
		wlQuota: wlQuotaFn,
	}.CascadeDelete

	// 环境（物理隔离单元 prod|test）：方法级权限 environment:read/write + prod 写校验。
	// 同时作 EnvTypeResolver 横切注入到 workload/devops/appconfig/governance/dataservice。
	envHandler := environment.NewHandler(stores.Environment)
	envHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/environments", auth(envHandler))
	mux.Handle("/api/environments/", auth(envHandler))

	// 工作负载：注入 environment store 作 EnvTypeResolver，启用生产写校验（dev 生产只读）。
	// stores.Workload 与 DevOps 共享：Release 编排要更新 Workload.ImageRef。
	// statusReader 注入 K8s 实际状态读取器：List 时回填真实 Ready/Status（覆盖 store 静态值），
	// clientset 为 nil（非集群部署）时 NewK8sStatusReader 内部 no-op，透传 store 原值（降级）。
	wlHandler := workload.NewHandler(stores.Workload,
		workload.WithEnvResolver(stores.Environment),
		workload.WithStatusReader(statusReader),
	)
	wlHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// 横切配额拦截：创建工作负载前调 billing.CheckAndInc，超限回 429。
	wlHandler.QuotaCheck = func(ctx context.Context, delta int) error {
		_, err := stores.Billing.CheckAndInc(ctx, billing.ResWorkloads, delta)
		return err
	}

	// DevOps（代码->构建->镜像->发布->回滚）：注入 environment store（prod 写校验）+ UserIDFrom（填发布人）。
	// stores.DevOps* 四子接口由同一 store 实现（内存/PG 同构）；Release 编排经 workload 仓储接口透明。
	// Gitea/Registry client（一站式：内置 Git 后端 + 镜像库实时视图），env 未配则 nil（功能降级）。
	var giteaClient *gitea.Client
	if u := os.Getenv("PAAS_GITEA_URL"); u != "" {
		giteaClient = gitea.New(u, os.Getenv("PAAS_GITEA_USER"), os.Getenv("PAAS_GITEA_PASSWORD"))
		log.Printf("[devops] gitea client: %s", u)
	}
	var registryClient *registry.Client
	if u := os.Getenv("PAAS_REGISTRY"); u != "" {
		registryClient = registry.New(u)
		log.Printf("[devops] registry client: %s", u)
	}
	devopsHandler := devops.NewHandler(stores.DevOpsRepos, stores.DevOpsBuilds, stores.DevOpsImages, stores.DevOpsReleases,
		devops.WithEnvResolver(stores.Environment),
		devops.WithEnvPromoter(stores.Environment),
		devops.WithUserIDFrom(gateway.UserIDFrom),
		devops.WithGiteaClient(giteaClient),
		devops.WithRegistryClient(registryClient),
	)
	devopsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 流水线引擎（变更→构建→发布→部署→测试→写基线，可自定义，每应用多条）：
	// engine 经 buildBridge/releaseBridge/giteaBridge 桥接 devops 业务包（依赖倒置破循环）；
	// handler 注入 EnvTypeResolver（prod deploy 校验）+ RepoResolver（build stage 解析内置仓库）+
	// identityAuditAdapter（审计，与 identity/devops 同源）+ actor（gateway.UserIDFrom）。
	pipeEngine := &pipeline.Engine{
		Pipelines: stores.Pipeline, Runs: stores.Pipeline,
		Builds:   &buildBridge{builds: stores.DevOpsBuilds},
		Releases: &releaseBridge{
			releases: stores.DevOpsReleases, images: stores.DevOpsImages,
			workloads: stores.Workload, envs: stores.Environment,
		},
		Gitea: &giteaBridge{repos: stores.DevOpsRepos, gitea: giteaClient},
	}
	pipelineHandler := pipeline.NewHandler(stores.Pipeline, stores.Pipeline, stores.Pipeline, pipeEngine,
		pipeline.WithAuthorize(func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }),
		pipeline.WithEnvType(envTypeBridge(stores.Environment)),
		pipeline.WithPromoteTargetType(promoteTargetTypeBridge(stores.Environment)),
		pipeline.WithRepoResolver(&repoResolverBridge{repos: stores.DevOpsRepos}),
		pipeline.WithParamResolver(&paramResolverBridge{apps: stores.Application, envs: stores.Environment, repos: stores.DevOpsRepos}),
		pipeline.WithAudit(&identityAuditAdapter{store: stores.Security}),
		pipeline.WithActorFn(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)
	// 应用创建后置 hook：自动建默认流水线绑定（tpl-ci/tpl-cd），best-effort。
	appHandler.OnAppCreate = defaultPipelineBinder(stores.Pipeline, stores.Pipeline)

	// 应用配置（工作负载级 env/Secret）：注入 environment store（prod 写校验）。
	// Secret 后端明文存储，API 返回固定掩码；与配置中心（服务治理，运行时动态）严格区分。
	appconfigHandler := appconfig.NewHandler(stores.AppConfig, appconfig.WithEnvResolver(stores.Environment))
	appconfigHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 服务治理（注册中心 = 平台能力横切）：注入 environment store（prod 写校验）。
	// 服务/实例租户私有；本期进程内 mock，数据面 SDK 接入留后续。
	govHandler := governance.NewHandler(stores.Governance, governance.WithEnvResolver(stores.Environment))
	govHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// 数据面 SDK 接入 API（/dp/）：把 K8s Endpoints 暴露为 zeus 兼容服务发现真源。
	// 鉴权复用 auth（dp token = API Key，绑 tenant）；reader 从 appliers.clientset 读 Endpoints
	// （非集群部署 clientset=nil，/dp/instances 降级返空，与现状一致不破坏）。
	dpHandler := dataplane.NewHandler(
		dataplane.NewEndpointsReader(appliers.clientset),
		stores.Governance,
	)
	mux.Handle("/dp/", auth(dpHandler))

	// 配置中心（治理四件套：运行时动态配置，版本/发布/回滚）。
	// 独立于物理环境（namespace 逻辑隔离），不接 EnvTypeResolver；复用 governance:read/write 权限。
	ccHandler := configcenter.NewHandler(stores.ConfigCenter)
	ccHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 可观测（指标监控 + 告警规则，平台能力横切）。
	// 惰性时序模拟采集，即时评估告警；不接 prod:write，独立于物理环境。
	obsRepo := buildObservabilityStore()
	obsHandler := observability.NewHandler(obsRepo)
	obsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 安全（密钥/证书 + 审计日志，平台能力横切）。
	// 注入 UserIDFrom 填审计 actor；Secret 明文存储/掩码返回，写操作自动记审计。
	// 平台级 Secret（scope=platform，如 sec-platform-airouter 全租户推理凭证）写操作需 super_admin：
	// 用 IsPlatformAdmin 而非 IsAdmin——后者校验 tenant:admin，每个租户的 admin 都持有，
	// 会导致任意 tenant-admin 可删除/伪造全平台共享凭证（越权）。stores.Security 同时桥接为 CredentialResolver。
	secHandler := security.NewHandler(stores.Security, security.WithUserIDFrom(gateway.UserIDFrom), security.WithIsAdmin(gateway.IsPlatformAdmin))
	secHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 配额计费（租户级资源配额 + 用量 + 账单，多租户商业化根基）。
	// 独立于物理环境，不接 prod:write；权限 billing:read/write（admin 写，dev/view 读）。
	// stores.Billing 与资源 Create 配额拦截共享同一 Store（用量真源唯一）。
	billingHandler := billing.NewHandler(stores.Billing)
	billingHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 数据服务（资源中心：DB/缓存/MQ/存储/向量/搜索，通用领域 + Kind 区分）。
	// 注入 environment store（prod 写校验）+ K8s restarter（实例滚动重启，集群外 nil 降级）+
	// 引擎目录（Create 按 engineID 解析）；权限 dataservice:read/write。
	dsOpts := []dataservice.HandlerOpt{
		dataservice.WithEnvResolver(stores.Environment),
		dataservice.WithEngineRepo(stores.Engine),
	}
	if appliers.dsRestarter != nil { // typed nil 防御：*DSRestarter(nil) 包成接口后 != nil
		dsOpts = append(dsOpts, dataservice.WithRestarter(appliers.dsRestarter))
	}
	dsHandler := dataservice.NewHandler(stores.DataService, dsOpts...)
	dsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// admin dataservice handler（L1 详情+实例 / L2 启停·重启·扩缩·删 / L3 代建）。
	// 全挂 adminGuard(super_admin)；绕过 prod:write；写操作记审计；代建消耗目标租户配额。
	// typed nil 防御：*DSRestarter(nil) 包成接口后 != nil（与租户端 dsOpts L491 同款）。
	// 集群外部署（无 clientset）时不注入 restarter -> handler 内 h.restarter==nil -> 返 503 友好降级，
	// 避免装箱接口 != nil 致守护失效 -> (*DSRestarter)(nil).Restart 访问 nil client panic。
	dsAdminOpts := []dataservice.AdminHandlerOpt{
		dataservice.WithAdminEngineRepo(stores.Engine),
		dataservice.WithAdminInstances(dsInstanceReader{r: dataplane.NewEndpointsReader(appliers.clientset)}),
		dataservice.WithAdminQuota(quotaCheckFn(stores.Billing)),
		dataservice.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		dataservice.WithAdminTenants(tenantChecker{repo: stores.Identity}),
		dataservice.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	}
	if appliers.dsRestarter != nil {
		dsAdminOpts = append(dsAdminOpts, dataservice.WithAdminRestarter(appliers.dsRestarter))
	}
	dsAdminHandler := dataservice.NewAdminHandler(stores.DataService, dsAdminOpts...)

	// admin environment handler（L3 代建）。环境是基础设施，admin 可代某租户建环境。
	envAdminHandler := environment.NewAdminHandler(stores.Environment,
		environment.WithAdminTenants(tenantChecker{repo: stores.Identity}),
		environment.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		environment.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// 引擎目录 handler：/api/engines（用户 enabled 列表）+ /api/admin/engines（super_admin CRUD）。
	engineHandler := dataservice.NewEngineHandler(stores.Engine)
	engineHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// admin 引擎写操作审计（平台级，super_admin 改 engine 目录记审计，与 model/vendor 同款）。
	engineHandler.SetAdminAudit(&identityAuditAdapter{store: stores.Security}).SetAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) })

	// admin workload handler（L1 详情+实例+日志 / L2 扩缩容·删除）。
	// 全挂 adminGuard(super_admin)；绕过 prod:write；写操作记审计；删除回收配额。
	// 复用 statusReader（与 wlHandler 同源 K8s clientset/namespace）+ identityAuditAdapter + tenantChecker。
	wlAdminHandler := workload.NewAdminHandler(stores.Workload,
		workload.WithAdminStatusReader(statusReader),
		workload.WithAdminQuota(wlQuotaFn),
		workload.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		workload.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin devops handler（构建/镜像/发布 详情 + 回滚）。不代建（业务编排类）。
	// 全挂 adminGuard(super_admin)；绕过 prod:write（super_admin 有权干预生产）；写操作记审计。
	// 复用 identityAuditAdapter + actor wrapper。BuildRun/Image/Release Repository 无 Delete 方法 -> 不提供删除；
	// BuildRun 重试涉及异步构建流转（baseCtx/pipeline），admin handler 内不干净复用，YAGNI 跳过。
	devopsAdminHandler := devops.NewAdminHandler(stores.DevOpsBuilds, stores.DevOpsImages, stores.DevOpsReleases,
		devops.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		devops.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin application handler（L1 详情 / L2 删除）。
	// 业务编排类不代建（基线 spec 明确）。级联清理复用 appCascadeDeleter（与租户侧 appHandler.CascadeDelete 同一实现，注入 wlQuota 回收 workload 配额）。
	appAdminHandler := application.NewAdminHandler(stores.Application,
		application.WithAdminQuota(func(ctx context.Context, delta int) error {
			_, err := stores.Billing.CheckAndInc(ctx, billing.ResApplications, delta)
			return err
		}),
		application.WithAdminCascade(appCascadeDeleter{wl: stores.Workload, cfg: stores.AppConfig, wlQuota: wlQuotaFn}),
		application.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		application.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin governance handler（L1 服务详情+实例 / L2 注销实例·删服务）。
	// 全挂 adminGuard(super_admin)；绕过 prod:write；写操作记审计。路由/熔断无 ListAll -> 只做服务。
	govAdminHandler := governance.NewAdminHandler(stores.Governance,
		governance.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		governance.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin observability handler（L1 告警规则详情 / L2 删除）。无 UpdateAlertRule -> 启停跳过，只做删。
	obsAdminHandler := observability.NewAdminHandler(obsRepo,
		observability.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		observability.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin billing handler（配额列表+调整 / 账单列表+详情+标记已付）。
	// 全挂 adminGuard(super_admin)；绕过 prod:write；写操作记审计。调整配额不消耗配额（SetQuota 改 Limits）。
	// 注入 tenantChecker 校验租户存在（防给不存在租户设配额污染数据，与 dataservice/environment 代建对齐）。
	billAdminHandler := billing.NewAdminHandler(stores.Billing,
		billing.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		billing.WithAdminTenants(tenantChecker{repo: stores.Identity}),
		billing.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// admin security handler（L1 密钥详情（掩码）/ L2 删除）。平台级 Secret TenantID 空，target_tenant 记空。
	secAdminHandler := security.NewAdminHandler(stores.Security,
		security.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		security.WithAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
	)

	// composite：按 /api/applications/{id}/{sub} 的 sub 段分发到对应 handler。
	// workloads -> 工作负载；repositories/buildruns/images/releases -> DevOps；其余（bindings 等）-> 应用。
	composite := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest := strings.TrimPrefix(r.URL.Path, "/api/applications/"); rest != r.URL.Path {
			parts := strings.Split(strings.Trim(rest, "/"), "/")
			if len(parts) >= 2 {
				switch parts[1] {
				case "workloads":
					wlHandler.ServeHTTP(w, r)
					return
				case "repositories", "buildruns", "images", "releases":
					devopsHandler.ServeHTTP(w, r)
					return
				case "pipelines":
					pipelineHandler.ServeHTTP(w, r)
					return
				case "configs":
					appconfigHandler.ServeHTTP(w, r)
					return
				}
			}
		}
		appHandler.ServeHTTP(w, r)
	})
	mux.Handle("/api/applications", auth(composite))
	mux.Handle("/api/applications/", auth(composite))
	mux.Handle("/api/workloads", auth(wlHandler))
	mux.Handle("/api/workloads/", auth(wlHandler))
	// DevOps 详情/动作路由（非应用作用域）：构建详情 / 镜像详情 / 回滚
	mux.Handle("/api/buildruns", auth(devopsHandler))
	mux.Handle("/api/buildruns/", auth(devopsHandler))
	mux.Handle("/api/images", auth(devopsHandler))
	mux.Handle("/api/images/", auth(devopsHandler))
	mux.Handle("/api/releases", auth(devopsHandler))
	mux.Handle("/api/releases/", auth(devopsHandler))
	// 流水线（平台预置模板 + run 列表/详情/审批/终止）。应用作用域 pipelines 经 composite 分发。
	mux.Handle("/api/pipeline-templates", auth(pipelineHandler))
	mux.Handle("/api/pipelineruns", auth(pipelineHandler))
	mux.Handle("/api/pipelineruns/", auth(pipelineHandler))
	// 镜像库实时视图（registry v2 catalog/tags），复用 devops handler 分发 + image:read 权限。
	mux.Handle("/api/registry", auth(devopsHandler))
	mux.Handle("/api/registry/", auth(devopsHandler))
	// 服务治理（注册中心）
	mux.Handle("/api/services", auth(govHandler))
	mux.Handle("/api/services/", auth(govHandler))
	mux.Handle("/api/instances", auth(govHandler))
	mux.Handle("/api/instances/", auth(govHandler))
	// 服务治理（API 网关路由）
	mux.Handle("/api/routes", auth(govHandler))
	mux.Handle("/api/routes/", auth(govHandler))
	// 服务治理（熔断器，治理四件套之熔断）
	mux.Handle("/api/breakers", auth(govHandler))
	mux.Handle("/api/breakers/", auth(govHandler))
	// 配置中心（治理：动态配置 + 版本/发布/回滚）
	mux.Handle("/api/configcenter/namespaces", auth(ccHandler))
	mux.Handle("/api/configcenter/namespaces/", auth(ccHandler))
	mux.Handle("/api/configcenter/publishes", auth(ccHandler))
	mux.Handle("/api/configcenter/publishes/", auth(ccHandler))
	// 可观测（指标 + 告警）
	mux.Handle("/api/observability/metrics", auth(obsHandler))
	mux.Handle("/api/observability/alert-rules", auth(obsHandler))
	mux.Handle("/api/observability/alert-rules/", auth(obsHandler))
	mux.Handle("/api/observability/alerts", auth(obsHandler))
	mux.Handle("/api/observability/logs", auth(obsHandler))
	mux.Handle("/api/observability/traces", auth(obsHandler))
	// 安全（密钥/证书 + 审计）
	mux.Handle("/api/security/secrets", auth(secHandler))
	mux.Handle("/api/security/secrets/", auth(secHandler))
	mux.Handle("/api/security/audit-logs", auth(secHandler))
	// 配额计费（配额/用量/账单）
	mux.Handle("/api/billing/quota", auth(billingHandler))
	mux.Handle("/api/billing/usage", auth(billingHandler))
	mux.Handle("/api/billing/records", auth(billingHandler))
	mux.Handle("/api/billing/records/", auth(billingHandler))
	// 数据服务（资源中心 CRUD + meta + 实例管理）
	mux.Handle("/api/dataservices", auth(dsHandler))
	mux.Handle("/api/dataservices/", auth(dsHandler))
	// 引擎目录：用户 enabled 列表（创建表单用，auth 即可）+ admin CRUD（adminGuard super_admin）
	mux.Handle("/api/engines", auth(engineHandler))
	mux.Handle("/api/admin/engines", adminGuard(engineHandler))
	mux.Handle("/api/admin/engines/", adminGuard(engineHandler))

	// 模型管理（平台级，super_admin 由 adminGuard 兜底）：模型/通道 CRUD + 写后增量刷新 gateway 路由表。
	// admin 模型/通道/供应商写操作审计（平台级，super_admin 改 model/channel/vendor 记审计，
	// 合规「审计只增不删」——凭证/路由配置类敏感操作必须有审计轨迹，与 identity P1.4 同款）。
	maasHandler := maas.NewHandler(stores.MaaS, gw, secretResolver{store: stores.Security.(security.SecretStore)}).
		SetAdminAudit(&identityAuditAdapter{store: stores.Security}).
		SetAdminActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) })
	mux.Handle("/api/admin/models", adminGuard(maasHandler))
	mux.Handle("/api/admin/models/", adminGuard(maasHandler))
	// 模型管理 spec（composite 内部按 method+path 分发，mux 已 subtree 注册，此处仅记 OpenAPI）
	reg.Operation("GET", "/api/admin/models", apiroute.Tags("模型管理"), apiroute.Summary("模型列表"), apiroute.Perm("super_admin"), apiroute.WithResp([]provider.Model{}))
	reg.Operation("POST", "/api/admin/models", apiroute.Tags("模型管理"), apiroute.Summary("创建模型"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Model{}), apiroute.WithResp(provider.Model{}))
	reg.Operation("GET", "/api/admin/models/{id}", apiroute.Tags("模型管理"), apiroute.Summary("模型详情"), apiroute.Perm("super_admin"), apiroute.WithResp(provider.Model{}))
	reg.Operation("PUT", "/api/admin/models/{id}", apiroute.Tags("模型管理"), apiroute.Summary("更新模型标量"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Model{}), apiroute.WithResp(provider.Model{}))
	reg.Operation("DELETE", "/api/admin/models/{id}", apiroute.Tags("模型管理"), apiroute.Summary("删除模型（级联清通道）"), apiroute.Perm("super_admin"))
	reg.Operation("GET", "/api/admin/models/{id}/channels", apiroute.Tags("模型管理"), apiroute.Summary("通道列表"), apiroute.Perm("super_admin"), apiroute.WithResp([]provider.Channel{}))
	reg.Operation("POST", "/api/admin/models/{id}/channels", apiroute.Tags("模型管理"), apiroute.Summary("创建通道"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Channel{}), apiroute.WithResp(provider.Channel{}))
	reg.Operation("PUT", "/api/admin/models/{id}/channels/{cid}", apiroute.Tags("模型管理"), apiroute.Summary("更新通道"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Channel{}), apiroute.WithResp(provider.Channel{}))
	reg.Operation("DELETE", "/api/admin/models/{id}/channels/{cid}", apiroute.Tags("模型管理"), apiroute.Summary("删除通道"), apiroute.Perm("super_admin"))

	// 供应商管理（Vendor：预设 BaseURL+凭证+Type，创建通道选供应商即带入，免去手填）。
	mux.Handle("/api/admin/providers", adminGuard(maasHandler))
	mux.Handle("/api/admin/providers/", adminGuard(maasHandler))
	reg.Operation("GET", "/api/admin/providers", apiroute.Tags("供应商管理"), apiroute.Summary("供应商列表"), apiroute.Perm("super_admin"), apiroute.WithResp([]provider.Vendor{}))
	reg.Operation("POST", "/api/admin/providers", apiroute.Tags("供应商管理"), apiroute.Summary("创建供应商"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Vendor{}), apiroute.WithResp(provider.Vendor{}))
	reg.Operation("GET", "/api/admin/providers/{id}", apiroute.Tags("供应商管理"), apiroute.Summary("供应商详情"), apiroute.Perm("super_admin"), apiroute.WithResp(provider.Vendor{}))
	reg.Operation("PUT", "/api/admin/providers/{id}", apiroute.Tags("供应商管理"), apiroute.Summary("更新供应商"), apiroute.Perm("super_admin"), apiroute.WithReqBody(provider.Vendor{}), apiroute.WithResp(provider.Vendor{}))
	reg.Operation("DELETE", "/api/admin/providers/{id}", apiroute.Tags("供应商管理"), apiroute.Summary("删除供应商"), apiroute.Perm("super_admin"))

	// 知识库（RAG）：KB/Document/检索。复用 MaaS embedding + qdrant/minio（env 配共享实例）。
	// PAAS_KB_QDRANT_URL / PAAS_KB_MINIO_ENDPOINT 非空才装配 retriever/blob（否则文档上传/检索 503 降级，KB CRUD 仍可用）。
	// 多租户隔离靠 collection（kb_{kbID}）+ bucket（kb-{tenant}）名，共享 qdrant/minio 实例。多实例留后续。
	var kbRetriever *knowledgebase.Retriever
	var kbBlob knowledgebase.BlobStore
	if qdrantURL := os.Getenv("PAAS_KB_QDRANT_URL"); qdrantURL != "" {
		kbRetriever = knowledgebase.NewRetriever(
			stores.KnowledgeBase,
			newQdrantVectorStore(qdrantURL, os.Getenv("PAAS_KB_QDRANT_API_KEY")),
			newMaasEmbedderFactory(stores.MaaS, secretResolver{store: stores.Security.(security.SecretStore)}),
		)
	}
	if minioEp := os.Getenv("PAAS_KB_MINIO_ENDPOINT"); minioEp != "" {
		if mb, err := newMinioBlobStore(minioEp, os.Getenv("PAAS_KB_MINIO_ACCESS_KEY"), os.Getenv("PAAS_KB_MINIO_SECRET_KEY")); err == nil {
			kbBlob = mb
		} else {
			log.Printf("KB minio 初始化失败: %v", err)
		}
	}
	kbHandler := knowledgebase.NewHandler(stores.KnowledgeBase, kbRetriever, kbBlob)
	kbHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	kbHandler.WithBaseCtx(context.Background())
	mux.Handle("/api/knowledgebases", auth(kbHandler))
	mux.Handle("/api/knowledgebases/", auth(kbHandler))
	reg.Operation("GET", "/api/knowledgebases", apiroute.Tags("知识库"), apiroute.Summary("知识库列表"), apiroute.Perm("kb:read"), apiroute.WithResp([]knowledgebase.KnowledgeBase{}))
	reg.Operation("POST", "/api/knowledgebases", apiroute.Tags("知识库"), apiroute.Summary("创建知识库"), apiroute.Perm("kb:write"), apiroute.WithReqBody(knowledgebase.KnowledgeBase{}), apiroute.WithResp(knowledgebase.KnowledgeBase{}))
	reg.Operation("GET", "/api/knowledgebases/{id}", apiroute.Tags("知识库"), apiroute.Summary("知识库详情"), apiroute.Perm("kb:read"), apiroute.WithResp(knowledgebase.KnowledgeBase{}))
	reg.Operation("PUT", "/api/knowledgebases/{id}", apiroute.Tags("知识库"), apiroute.Summary("更新知识库"), apiroute.Perm("kb:write"), apiroute.WithReqBody(knowledgebase.KnowledgeBase{}), apiroute.WithResp(knowledgebase.KnowledgeBase{}))
	reg.Operation("DELETE", "/api/knowledgebases/{id}", apiroute.Tags("知识库"), apiroute.Summary("删除知识库（级联清文档+chunks+向量）"), apiroute.Perm("kb:write"))
	reg.Operation("GET", "/api/knowledgebases/{id}/documents", apiroute.Tags("知识库"), apiroute.Summary("文档列表"), apiroute.Perm("kb:read"), apiroute.WithResp([]knowledgebase.Document{}))
	reg.Operation("POST", "/api/knowledgebases/{id}/documents", apiroute.Tags("知识库"), apiroute.Summary("上传文档（异步解析+索引）"), apiroute.Perm("kb:write"), apiroute.WithResp(knowledgebase.Document{}))
	reg.Operation("GET", "/api/knowledgebases/{id}/documents/{docId}", apiroute.Tags("知识库"), apiroute.Summary("文档状态"), apiroute.Perm("kb:read"), apiroute.WithResp(knowledgebase.Document{}))
	reg.Operation("DELETE", "/api/knowledgebases/{id}/documents/{docId}", apiroute.Tags("知识库"), apiroute.Summary("删除文档（清chunks+向量+原文）"), apiroute.Perm("kb:write"))
	reg.Operation("POST", "/api/knowledgebases/{id}/retrieve", apiroute.Tags("知识库"), apiroute.Summary("检索"), apiroute.Perm("kb:read"), apiroute.WithResp([]knowledgebase.ChunkHit{}))

	// AI 工具管理（P2）：Agent 可调用的外部能力（MCP server / HTTP / 内置）。
	// 租户私有；test/invoke 仅 mcp 类型（initialize + tools/list + tools/call）。
	toolHandler := tool.NewHandler(stores.Tool)
	toolHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/tools", auth(toolHandler))
	mux.Handle("/api/tools/", auth(toolHandler))
	reg.Operation("GET", "/api/tools", apiroute.Tags("AI工具"), apiroute.Summary("工具列表"), apiroute.Perm("tool:read"), apiroute.WithResp([]tool.Tool{}))
	reg.Operation("POST", "/api/tools", apiroute.Tags("AI工具"), apiroute.Summary("创建工具"), apiroute.Perm("tool:write"), apiroute.WithReqBody(tool.Tool{}), apiroute.WithResp(tool.Tool{}))
	reg.Operation("GET", "/api/tools/{id}", apiroute.Tags("AI工具"), apiroute.Summary("工具详情"), apiroute.Perm("tool:read"), apiroute.WithResp(tool.Tool{}))
	reg.Operation("PUT", "/api/tools/{id}", apiroute.Tags("AI工具"), apiroute.Summary("更新工具"), apiroute.Perm("tool:write"), apiroute.WithReqBody(tool.Tool{}), apiroute.WithResp(tool.Tool{}))
	reg.Operation("DELETE", "/api/tools/{id}", apiroute.Tags("AI工具"), apiroute.Summary("删除工具"), apiroute.Perm("tool:write"))
	reg.Operation("POST", "/api/tools/{id}/test", apiroute.Tags("AI工具"), apiroute.Summary("测试工具（MCP: initialize+tools/list）"), apiroute.Perm("tool:read"))
	reg.Operation("POST", "/api/tools/{id}/invoke", apiroute.Tags("AI工具"), apiroute.Summary("调用工具（MCP: tools/call）"), apiroute.Perm("tool:read"))

	// Prompt 模板管理（P2）：版本化提示词，同 name 多版本，最新版自动激活。
	promptHandler := prompt.NewHandler(stores.Prompt)
	promptHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/prompts", auth(promptHandler))
	mux.Handle("/api/prompts/", auth(promptHandler))
	reg.Operation("GET", "/api/prompts", apiroute.Tags("Prompt"), apiroute.Summary("Prompt 列表（全部版本）"), apiroute.Perm("prompt:read"), apiroute.WithResp([]prompt.Prompt{}))
	reg.Operation("POST", "/api/prompts", apiroute.Tags("Prompt"), apiroute.Summary("创建 Prompt（同 name 自动 version+1 且激活）"), apiroute.Perm("prompt:write"), apiroute.WithReqBody(prompt.Prompt{}), apiroute.WithResp(prompt.Prompt{}))
	reg.Operation("GET", "/api/prompts/active", apiroute.Tags("Prompt"), apiroute.Summary("取激活版本（?name=）"), apiroute.Perm("prompt:read"), apiroute.WithResp(prompt.Prompt{}))
	reg.Operation("GET", "/api/prompts/{id}", apiroute.Tags("Prompt"), apiroute.Summary("取单版本"), apiroute.Perm("prompt:read"), apiroute.WithResp(prompt.Prompt{}))
	reg.Operation("DELETE", "/api/prompts/{id}", apiroute.Tags("Prompt"), apiroute.Summary("删单版本"), apiroute.Perm("prompt:write"))
	reg.Operation("POST", "/api/prompts/{id}/activate", apiroute.Tags("Prompt"), apiroute.Summary("激活该版本"), apiroute.Perm("prompt:write"), apiroute.WithResp(prompt.Prompt{}))

	// AI Agent（P3）：组装 system prompt + 工具描述 + KB RAG 调底层 LLM。
	// runtime 注入 MaaS（取 Provider）+ 凭证 + prompt/tool/KB（组装上下文）。
	agentRuntime := agent.NewRuntime(stores.Agent, stores.MaaS, secretResolver{store: stores.Security.(security.SecretStore)}, stores.Prompt, stores.Tool, kbRetriever).
		WithGuard(guardrail.NewFromEnv()).
		WithPromptLog(os.Getenv("PAAS_AI_LOG_PROMPTS") == "true")
	// 注入 gateway 虚拟模型路由：/v1/chat/completions 收 model="agent:{id}" 时转交 runtime。
	agentDispatcherHolder.Set(agentDispatcherAdapter{rt: agentRuntime})
	agentHandler := agent.NewHandler(stores.Agent, agentRuntime)
	agentHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/agents", auth(agentHandler))
	mux.Handle("/api/agents/", auth(agentHandler))
	reg.Operation("GET", "/api/agents", apiroute.Tags("Agent"), apiroute.Summary("Agent 列表"), apiroute.Perm("agent:read"), apiroute.WithResp([]agent.Agent{}))
	reg.Operation("POST", "/api/agents", apiroute.Tags("Agent"), apiroute.Summary("创建 Agent"), apiroute.Perm("agent:write"), apiroute.WithReqBody(agent.Agent{}), apiroute.WithResp(agent.Agent{}))
	reg.Operation("GET", "/api/agents/{id}", apiroute.Tags("Agent"), apiroute.Summary("Agent 详情"), apiroute.Perm("agent:read"), apiroute.WithResp(agent.Agent{}))
	reg.Operation("PUT", "/api/agents/{id}", apiroute.Tags("Agent"), apiroute.Summary("更新 Agent"), apiroute.Perm("agent:write"), apiroute.WithReqBody(agent.Agent{}), apiroute.WithResp(agent.Agent{}))
	reg.Operation("DELETE", "/api/agents/{id}", apiroute.Tags("Agent"), apiroute.Summary("删除 Agent"), apiroute.Perm("agent:write"))
	reg.Operation("POST", "/api/agents/{id}/run", apiroute.Tags("Agent"), apiroute.Summary("运行 Agent（SSE 流式，OpenAI 兼容）"), apiroute.Perm("agent:write"))

	// AI 评估（P4）：为 Agent 定义测试用例 + 批量跑测评分。service 注入 agentRuntime 作 Runner。
	evalSvc := eval.NewService(stores.Eval, agentRuntime)
	evalHandler := eval.NewHandler(stores.Eval, evalSvc)
	evalHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/agent-evals", auth(evalHandler))
	mux.Handle("/api/agent-evals/", auth(evalHandler))
	reg.Operation("GET", "/api/agent-evals", apiroute.Tags("评估"), apiroute.Summary("评估用例列表（?agentId=）"), apiroute.Perm("agent:read"), apiroute.WithResp([]eval.EvalCase{}))
	reg.Operation("POST", "/api/agent-evals", apiroute.Tags("评估"), apiroute.Summary("创建评估用例"), apiroute.Perm("agent:write"), apiroute.WithReqBody(eval.EvalCase{}), apiroute.WithResp(eval.EvalCase{}))
	reg.Operation("DELETE", "/api/agent-evals/{id}", apiroute.Tags("评估"), apiroute.Summary("删除评估用例"), apiroute.Perm("agent:write"))
	reg.Operation("POST", "/api/agent-evals/run", apiroute.Tags("评估"), apiroute.Summary("跑某 Agent 全部用例（?agentId=）"), apiroute.Perm("agent:read"), apiroute.WithResp([]eval.EvalResult{}))

	mux.Handle("/livez", health.NewHandler())

	// —— OpenAPI 元数据声明（Operation：spec-only，mux 注册见上方各 mux.Handle）——
	// 应用主线（composite 内部派发，mux 粗粒度已注册，此处补 spec 逻辑操作）
	reg.Operation("GET", "/api/applications",
		apiroute.Tags("应用"), apiroute.Summary("列出当前租户应用"), apiroute.Perm("application:read"),
		apiroute.WithResp([]application.Application{}))
	reg.Operation("POST", "/api/applications",
		apiroute.Tags("应用"), apiroute.Summary("创建应用"), apiroute.Perm("application:write"),
		apiroute.WithReqBody(application.Application{}), apiroute.WithResp(application.Application{}))
	reg.Operation("GET", "/api/applications/{id}",
		apiroute.Tags("应用"), apiroute.Summary("应用详情"), apiroute.Perm("application:read"),
		apiroute.WithResp(application.Application{}))
	reg.Operation("DELETE", "/api/applications/{id}",
		apiroute.Tags("应用"), apiroute.Summary("删除应用（级联清工作负载+配置）"), apiroute.Perm("application:write"))
	reg.Operation("POST", "/api/applications/{id}/bindings",
		apiroute.Tags("应用"), apiroute.Summary("绑定资源到应用"), apiroute.Perm("binding:write"),
		apiroute.WithReqBody(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{}), apiroute.WithResp(application.Application{}))
	reg.Operation("DELETE", "/api/applications/{id}/bindings/{type}/{name}",
		apiroute.Tags("应用"), apiroute.Summary("解绑应用资源"), apiroute.Perm("binding:write"),
		apiroute.WithResp(application.Application{}))
	// 环境（同上：mux 粗粒度注册，spec 补逻辑操作）
	reg.Operation("GET", "/api/environments",
		apiroute.Tags("环境"), apiroute.Summary("列出环境"), apiroute.Perm("environment:read"),
		apiroute.WithResp([]environment.Environment{}))
	reg.Operation("POST", "/api/environments",
		apiroute.Tags("环境"), apiroute.Summary("创建环境"), apiroute.Perm("environment:write"),
		apiroute.WithReqBody(environment.Environment{}), apiroute.WithResp(environment.Environment{}))
	reg.Operation("GET", "/api/environments/{id}",
		apiroute.Tags("环境"), apiroute.Summary("环境详情"), apiroute.Perm("environment:read"),
		apiroute.WithResp(environment.Environment{}))
	reg.Operation("DELETE", "/api/environments/{id}",
		apiroute.Tags("环境"), apiroute.Summary("删除环境"), apiroute.Perm("environment:write"))

	// /openapi.json：公开契约，无鉴权。
	mux.Handle("/openapi.json", apiroute.ServeSpec(reg))
	// /docs：Scalar 交互文档（拉 /openapi.json 渲染），公开无鉴权。
	mux.Handle("/docs", apiroute.ServeDocs("/openapi.json", "PaaS API"))

	// 未知 /api/* 与 /v1/* 返回 404 JSON，而非兜底到 SPA 的 index.html。
	// 前端 axios 收到 HTML（200）会把字符串当响应数据，下游 .filter/.map 在非数组上崩溃白屏；
	// 返回干净 404 JSON 让拦截器走错误分支优雅降级。ServeMux 最长前缀匹配：已注册的具体 API 路径不受影响。
	apiNotFound := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":"route not found: %s"}`, r.URL.Path) //nolint:gosec // G705: 404 错误响应写 URL.Path 到 JSON（Content-Type application/json），无 XSS 风险
	}
	mux.HandleFunc("/api/", apiNotFound)
	mux.HandleFunc("/v1/", apiNotFound)

	// 嵌入式前端 SPA（core 单镜像同域 serve，无 CORS）。API 路由已在上文注册，
	// ServeMux 最长前缀匹配使 /api/* /v1/* /openapi.json /docs /livez 优先命中；
	// 以下三者为剩余路径的前端入口，/ 兜底 landing。
	//   /console/* → console-user   /admin/* → console-admin   /* → landing
	mux.Handle("/console/", web.ServeStatic("/console/", "console-user"))
	mux.Handle("/admin/", web.ServeStatic("/admin/", "console-admin"))
	mux.Handle("/", web.ServeStatic("/", "landing"))

	// —— 其余模块 OpenAPI 元数据（Operation：spec-only，mux 注册见上方 mux.Handle）——
	// 工作负载（应用子资源 + 跨应用列表）
	reg.Operation("GET", "/api/applications/{id}/workloads", apiroute.Tags("工作负载"), apiroute.Summary("应用下工作负载"), apiroute.Perm("workload:read"), apiroute.WithResp([]workload.Workload{}))
	reg.Operation("POST", "/api/applications/{id}/workloads", apiroute.Tags("工作负载"), apiroute.Summary("创建工作负载"), apiroute.Perm("workload:write"), apiroute.WithReqBody(workload.Workload{}), apiroute.WithResp(workload.Workload{}))
	reg.Operation("GET", "/api/workloads", apiroute.Tags("工作负载"), apiroute.Summary("跨应用工作负载列表"), apiroute.Perm("workload:read"), apiroute.WithResp([]workload.Workload{}))
	reg.Operation("GET", "/api/workloads/{id}", apiroute.Tags("工作负载"), apiroute.Summary("工作负载详情（含运行实例）"), apiroute.Perm("workload:read"), apiroute.WithResp(workload.Detail{}))
	reg.Operation("GET", "/api/workloads/{id}/logs", apiroute.Tags("工作负载"), apiroute.Summary("实例（Pod）运行日志"), apiroute.Perm("workload:read"))
	reg.Operation("PUT", "/api/workloads/{id}", apiroute.Tags("工作负载"), apiroute.Summary("扩缩容/更新状态"), apiroute.Perm("workload:write"), apiroute.WithReqBody(struct {
		Replicas int    `json:"replicas"`
		Status   string `json:"status"`
	}{}), apiroute.WithResp(workload.Workload{}))
	reg.Operation("DELETE", "/api/workloads/{id}", apiroute.Tags("工作负载"), apiroute.Summary("删除工作负载"), apiroute.Perm("workload:write"))
	// DevOps
	reg.Operation("GET", "/api/applications/{id}/repositories", apiroute.Tags("DevOps"), apiroute.Summary("应用代码仓库"), apiroute.Perm("repository:read"), apiroute.WithResp([]devops.CodeRepo{}))
	reg.Operation("GET", "/api/applications/{id}/buildruns", apiroute.Tags("DevOps"), apiroute.Summary("应用构建记录"), apiroute.Perm("build:read"), apiroute.WithResp([]devops.BuildRun{}))
	reg.Operation("GET", "/api/applications/{id}/images", apiroute.Tags("DevOps"), apiroute.Summary("应用镜像"), apiroute.Perm("image:read"), apiroute.WithResp([]devops.Image{}))
	reg.Operation("GET", "/api/applications/{id}/releases", apiroute.Tags("DevOps"), apiroute.Summary("应用发布记录"), apiroute.Perm("release:read"), apiroute.WithResp([]devops.Release{}))
	reg.Operation("POST", "/api/applications/{id}/repositories", apiroute.Tags("DevOps"), apiroute.Summary("绑定代码仓库"), apiroute.Perm("repository:write"), apiroute.WithReqBody(devops.CodeRepo{}), apiroute.WithResp(devops.CodeRepo{}))
	reg.Operation("POST", "/api/applications/{id}/buildruns", apiroute.Tags("DevOps"), apiroute.Summary("触发构建"), apiroute.Perm("build:write"), apiroute.WithReqBody(devops.BuildRun{}), apiroute.WithResp(devops.BuildRun{}))
	reg.Operation("POST", "/api/applications/{id}/releases", apiroute.Tags("DevOps"), apiroute.Summary("创建发布（编排基线 Workload + 更新镜像）"), apiroute.Perm("release:write"), apiroute.WithReqBody(devops.ReleaseInput{}), apiroute.WithResp(devops.Release{}))
	reg.Operation("GET", "/api/buildruns", apiroute.Tags("DevOps"), apiroute.Summary("跨应用构建列表"), apiroute.Perm("build:read"), apiroute.WithResp([]devops.BuildRun{}))
	reg.Operation("GET", "/api/buildruns/{id}", apiroute.Tags("DevOps"), apiroute.Summary("构建详情（含日志）"), apiroute.Perm("build:read"), apiroute.WithResp(devops.BuildRun{}))
	reg.Operation("GET", "/api/images", apiroute.Tags("DevOps"), apiroute.Summary("跨应用镜像列表"), apiroute.Perm("image:read"), apiroute.WithResp([]devops.Image{}))
	reg.Operation("GET", "/api/images/{id}", apiroute.Tags("DevOps"), apiroute.Summary("镜像详情"), apiroute.Perm("image:read"), apiroute.WithResp(devops.Image{}))
	reg.Operation("GET", "/api/applications/{id}/repositories/{rid}/tree", apiroute.Tags("DevOps"), apiroute.Summary("内置仓库文件树（Gitea）"), apiroute.Perm("repository:read"), apiroute.WithResp([]gitea.TreeNode{}))
	reg.Operation("GET", "/api/applications/{id}/repositories/{rid}/commits", apiroute.Tags("DevOps"), apiroute.Summary("内置仓库提交历史（Gitea）"), apiroute.Perm("repository:read"), apiroute.WithResp([]gitea.Commit{}))
	reg.Operation("GET", "/api/applications/{id}/repositories/{rid}/file", apiroute.Tags("DevOps"), apiroute.Summary("内置仓库文件内容（?path=）"), apiroute.Perm("repository:read"))
	reg.Operation("GET", "/api/releases", apiroute.Tags("DevOps"), apiroute.Summary("跨应用发布列表"), apiroute.Perm("release:read"), apiroute.WithResp([]devops.Release{}))
	reg.Operation("POST", "/api/releases/{id}/rollback", apiroute.Tags("DevOps"), apiroute.Summary("回滚发布"), apiroute.Perm("release:write"), apiroute.WithResp(devops.Release{}))
	reg.Operation("POST", "/api/releases/{id}/promote", apiroute.Tags("DevOps"), apiroute.Summary("提升到下一阶环境（发布流水线）"), apiroute.Perm("release:write"), apiroute.WithResp(devops.Release{}))
	// 镜像库实时视图（registry v2）
	reg.Operation("GET", "/api/registry/repositories", apiroute.Tags("DevOps"), apiroute.Summary("镜像仓库 catalog（registry v2 实时）"), apiroute.Perm("image:read"), apiroute.WithResp([]string{}))
	reg.Operation("GET", "/api/registry/tags", apiroute.Tags("DevOps"), apiroute.Summary("镜像 tag+digest（?repository=）"), apiroute.Perm("image:read"))
	// 流水线（变更→构建→发布→部署→测试→写基线，可自定义；每应用多条）
	reg.Operation("GET", "/api/pipeline-templates", apiroute.Tags("流水线"), apiroute.Summary("流水线模板（平台预置 + 租户自定义）"), apiroute.Perm("pipeline:read"), apiroute.WithResp([]pipeline.PipelineTemplate{}))
	reg.Operation("GET", "/api/applications/{id}/pipelines", apiroute.Tags("流水线"), apiroute.Summary("应用流水线列表"), apiroute.Perm("pipeline:read"), apiroute.WithResp([]pipeline.Pipeline{}))
	reg.Operation("POST", "/api/applications/{id}/pipelines", apiroute.Tags("流水线"), apiroute.Summary("创建流水线（可从模板）"), apiroute.Perm("pipeline:write"), apiroute.WithReqBody(pipeline.Pipeline{}), apiroute.WithResp(pipeline.Pipeline{}))
	reg.Operation("GET", "/api/applications/{id}/pipelines/{pid}", apiroute.Tags("流水线"), apiroute.Summary("流水线详情"), apiroute.Perm("pipeline:read"), apiroute.WithResp(pipeline.Pipeline{}))
	reg.Operation("PUT", "/api/applications/{id}/pipelines/{pid}", apiroute.Tags("流水线"), apiroute.Summary("更新流水线"), apiroute.Perm("pipeline:write"), apiroute.WithReqBody(pipeline.Pipeline{}), apiroute.WithResp(pipeline.Pipeline{}))
	reg.Operation("DELETE", "/api/applications/{id}/pipelines/{pid}", apiroute.Tags("流水线"), apiroute.Summary("删除流水线"), apiroute.Perm("pipeline:write"))
	reg.Operation("POST", "/api/applications/{id}/pipelines/{pid}/run", apiroute.Tags("流水线"), apiroute.Summary("手动触发流水线运行"), apiroute.Perm("pipeline:write"), apiroute.WithReqBody(struct {
		Branch  string `json:"branch"`
		Commit  string `json:"commit"`
		Version string `json:"version"`
	}{}), apiroute.WithResp(pipeline.PipelineRun{}))
	reg.Operation("GET", "/api/pipelineruns", apiroute.Tags("流水线"), apiroute.Summary("运行列表（?appId=&pipelineId=&status=）"), apiroute.Perm("pipeline:read"), apiroute.WithResp([]pipeline.PipelineRun{}))
	reg.Operation("GET", "/api/pipelineruns/{id}", apiroute.Tags("流水线"), apiroute.Summary("运行详情（含各 stage 状态/输入输出）"), apiroute.Perm("pipeline:read"), apiroute.WithResp(pipeline.PipelineRun{}))
	reg.Operation("POST", "/api/pipelineruns/{id}/stages/{idx}/approve", apiroute.Tags("流水线"), apiroute.Summary("审批/人工确认通过（恢复 paused run）"), apiroute.Perm("pipeline:write"))
	reg.Operation("POST", "/api/pipelineruns/{id}/abort", apiroute.Tags("流水线"), apiroute.Summary("终止运行"), apiroute.Perm("pipeline:write"))
	// 应用配置
	reg.Operation("GET", "/api/applications/{id}/configs", apiroute.Tags("应用配置"), apiroute.Summary("应用配置项（掩码）"), apiroute.Perm("config:read"), apiroute.WithResp([]appconfig.ConfigItem{}))
	reg.Operation("POST", "/api/applications/{id}/configs", apiroute.Tags("应用配置"), apiroute.Summary("新增/更新配置项"), apiroute.Perm("config:write"), apiroute.WithReqBody(appconfig.ConfigItem{}), apiroute.WithResp(appconfig.ConfigItem{}))
	reg.Operation("DELETE", "/api/applications/{id}/configs/{cfgId}", apiroute.Tags("应用配置"), apiroute.Summary("删除配置项"), apiroute.Perm("config:write"))
	// 服务治理（注册中心 + API 网关路由 + 熔断）
	reg.Operation("GET", "/api/services", apiroute.Tags("服务治理"), apiroute.Summary("服务列表"), apiroute.Perm("governance:read"), apiroute.WithResp([]governance.Service{}))
	reg.Operation("POST", "/api/services", apiroute.Tags("服务治理"), apiroute.Summary("注册服务"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Service{}), apiroute.WithResp(governance.Service{}))
	reg.Operation("GET", "/api/services/{id}", apiroute.Tags("服务治理"), apiroute.Summary("服务详情"), apiroute.Perm("governance:read"), apiroute.WithResp(governance.ServiceDetail{}))
	reg.Operation("DELETE", "/api/services/{id}", apiroute.Tags("服务治理"), apiroute.Summary("注销服务"), apiroute.Perm("governance:write"))
	reg.Operation("POST", "/api/services/{id}/instances", apiroute.Tags("服务治理"), apiroute.Summary("注册实例"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Instance{}), apiroute.WithResp(governance.Instance{}))
	reg.Operation("DELETE", "/api/services/{id}/instances/{iid}", apiroute.Tags("服务治理"), apiroute.Summary("注销实例"), apiroute.Perm("governance:write"))
	reg.Operation("PUT", "/api/instances/{iid}/heartbeat", apiroute.Tags("服务治理"), apiroute.Summary("实例心跳"), apiroute.Perm("governance:write"))
	reg.Operation("GET", "/api/routes", apiroute.Tags("服务治理"), apiroute.Summary("API 网关路由列表"), apiroute.Perm("governance:read"), apiroute.WithResp([]governance.Route{}))
	reg.Operation("POST", "/api/routes", apiroute.Tags("服务治理"), apiroute.Summary("创建路由"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Route{}), apiroute.WithResp(governance.Route{}))
	reg.Operation("PUT", "/api/routes/{id}", apiroute.Tags("服务治理"), apiroute.Summary("更新路由"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Route{}), apiroute.WithResp(governance.Route{}))
	reg.Operation("DELETE", "/api/routes/{id}", apiroute.Tags("服务治理"), apiroute.Summary("删除路由"), apiroute.Perm("governance:write"))
	reg.Operation("GET", "/api/breakers", apiroute.Tags("服务治理"), apiroute.Summary("熔断器列表"), apiroute.Perm("governance:read"), apiroute.WithResp([]governance.CircuitBreaker{}))
	reg.Operation("POST", "/api/breakers", apiroute.Tags("服务治理"), apiroute.Summary("创建熔断器"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.CircuitBreaker{}), apiroute.WithResp(governance.CircuitBreaker{}))
	reg.Operation("PUT", "/api/breakers/{id}", apiroute.Tags("服务治理"), apiroute.Summary("更新熔断器"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.CircuitBreaker{}), apiroute.WithResp(governance.CircuitBreaker{}))
	reg.Operation("DELETE", "/api/breakers/{id}", apiroute.Tags("服务治理"), apiroute.Summary("删除熔断器"), apiroute.Perm("governance:write"))
	// 配置中心
	reg.Operation("GET", "/api/configcenter/namespaces", apiroute.Tags("配置中心"), apiroute.Summary("命名空间列表"), apiroute.Perm("governance:read"), apiroute.WithResp([]configcenter.Namespace{}))
	reg.Operation("POST", "/api/configcenter/namespaces", apiroute.Tags("配置中心"), apiroute.Summary("创建命名空间"), apiroute.Perm("governance:write"), apiroute.WithReqBody(configcenter.Namespace{}), apiroute.WithResp(configcenter.Namespace{}))
	reg.Operation("GET", "/api/configcenter/namespaces/{id}", apiroute.Tags("配置中心"), apiroute.Summary("命名空间详情"), apiroute.Perm("governance:read"), apiroute.WithResp(configcenter.Namespace{}))
	reg.Operation("DELETE", "/api/configcenter/namespaces/{id}", apiroute.Tags("配置中心"), apiroute.Summary("删除命名空间"), apiroute.Perm("governance:write"))
	reg.Operation("GET", "/api/configcenter/namespaces/{id}/items", apiroute.Tags("配置中心"), apiroute.Summary("配置项草稿"), apiroute.Perm("governance:read"), apiroute.WithResp([]configcenter.ConfigItem{}))
	reg.Operation("POST", "/api/configcenter/namespaces/{id}/items", apiroute.Tags("配置中心"), apiroute.Summary("新增/更新配置项（draft）"), apiroute.Perm("governance:write"), apiroute.WithReqBody(configcenter.ConfigItem{}), apiroute.WithResp(configcenter.ConfigItem{}))
	reg.Operation("POST", "/api/configcenter/namespaces/{id}/publish", apiroute.Tags("配置中心"), apiroute.Summary("发布配置版本"), apiroute.Perm("governance:write"), apiroute.WithResp(configcenter.Publish{}))
	reg.Operation("GET", "/api/configcenter/namespaces/{id}/published", apiroute.Tags("配置中心"), apiroute.Summary("当前生效配置（客户端发现）"), apiroute.Perm("governance:read"), apiroute.WithResp(configcenter.Publish{}))
	reg.Operation("GET", "/api/configcenter/publishes", apiroute.Tags("配置中心"), apiroute.Summary("发布历史"), apiroute.Perm("governance:read"), apiroute.WithResp([]configcenter.Publish{}))
	reg.Operation("POST", "/api/configcenter/publishes/{id}/rollback", apiroute.Tags("配置中心"), apiroute.Summary("回滚到历史版本"), apiroute.Perm("governance:write"), apiroute.WithResp(configcenter.Publish{}))
	// 可观测
	reg.Operation("GET", "/api/observability/metrics", apiroute.Tags("可观测"), apiroute.Summary("指标时序（惰性补点）"), apiroute.Perm("observability:read"), apiroute.WithResp([]observability.MetricSeries{}))
	reg.Operation("GET", "/api/observability/alert-rules", apiroute.Tags("可观测"), apiroute.Summary("告警规则列表"), apiroute.Perm("observability:read"), apiroute.WithResp([]observability.AlertRule{}))
	reg.Operation("POST", "/api/observability/alert-rules", apiroute.Tags("可观测"), apiroute.Summary("创建告警规则"), apiroute.Perm("observability:write"), apiroute.WithReqBody(observability.AlertRule{}), apiroute.WithResp(observability.AlertRule{}))
	reg.Operation("DELETE", "/api/observability/alert-rules/{id}", apiroute.Tags("可观测"), apiroute.Summary("删除告警规则"), apiroute.Perm("observability:write"))
	reg.Operation("GET", "/api/observability/alerts", apiroute.Tags("可观测"), apiroute.Summary("当前告警（即时评估）"), apiroute.Perm("observability:read"), apiroute.WithResp([]observability.Alert{}))
	reg.Operation("GET", "/api/observability/logs", apiroute.Tags("可观测"), apiroute.Summary("日志聚合（惰性补点）"), apiroute.Perm("observability:read"), apiroute.WithResp([]observability.LogEntry{}))
	reg.Operation("GET", "/api/observability/traces", apiroute.Tags("可观测"), apiroute.Summary("链路追踪（惰性生成）"), apiroute.Perm("observability:read"), apiroute.WithResp([]observability.Trace{}))
	// 安全（密钥/证书 + 审计）
	reg.Operation("GET", "/api/security/secrets", apiroute.Tags("安全"), apiroute.Summary("密钥/证书列表（掩码）"), apiroute.Perm("security:read"), apiroute.WithResp([]security.Secret{}))
	reg.Operation("POST", "/api/security/secrets", apiroute.Tags("安全"), apiroute.Summary("创建密钥/证书"), apiroute.Perm("security:write"), apiroute.WithReqBody(security.Secret{}), apiroute.WithResp(security.Secret{}))
	reg.Operation("DELETE", "/api/security/secrets/{id}", apiroute.Tags("安全"), apiroute.Summary("删除密钥/证书"), apiroute.Perm("security:write"))
	reg.Operation("GET", "/api/security/audit-logs", apiroute.Tags("安全"), apiroute.Summary("审计日志"), apiroute.Perm("security:read"), apiroute.WithResp([]security.AuditLog{}))
	// 配额计费
	reg.Operation("GET", "/api/billing/quota", apiroute.Tags("配额计费"), apiroute.Summary("租户配额"), apiroute.Perm("billing:read"), apiroute.WithResp(billing.ResourceQuota{}))
	reg.Operation("PUT", "/api/billing/quota", apiroute.Tags("配额计费"), apiroute.Summary("调整配额"), apiroute.Perm("billing:write"), apiroute.WithReqBody(billing.ResourceQuota{}), apiroute.WithResp(billing.ResourceQuota{}))
	reg.Operation("GET", "/api/billing/usage", apiroute.Tags("配额计费"), apiroute.Summary("用量视图（含超限标记）"), apiroute.Perm("billing:read"), apiroute.WithResp(billing.UsageView{}))
	reg.Operation("GET", "/api/billing/records", apiroute.Tags("配额计费"), apiroute.Summary("账单列表"), apiroute.Perm("billing:read"), apiroute.WithResp([]billing.BillingRecord{}))
	reg.Operation("POST", "/api/billing/records/generate", apiroute.Tags("配额计费"), apiroute.Summary("生成账单"), apiroute.Perm("billing:write"), apiroute.WithResp(billing.BillingRecord{}))
	reg.Operation("POST", "/api/billing/records/{id}/pay", apiroute.Tags("配额计费"), apiroute.Summary("支付账单"), apiroute.Perm("billing:write"), apiroute.WithResp(billing.BillingRecord{}))
	// 数据服务（资源中心）
	reg.Operation("GET", "/api/dataservices", apiroute.Tags("数据服务"), apiroute.Summary("资源列表（按 kind）"), apiroute.Perm("dataservice:read"), apiroute.WithResp([]dataservice.DataService{}))
	reg.Operation("GET", "/api/dataservices/meta", apiroute.Tags("数据服务"), apiroute.Summary("Kind 表单元数据"), apiroute.Perm("dataservice:read"))
	reg.Operation("POST", "/api/dataservices", apiroute.Tags("数据服务"), apiroute.Summary("创建数据服务"), apiroute.Perm("dataservice:write"), apiroute.WithReqBody(dataservice.DataService{}), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("GET", "/api/dataservices/{id}", apiroute.Tags("数据服务"), apiroute.Summary("数据服务详情"), apiroute.Perm("dataservice:read"), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("PUT", "/api/dataservices/{id}", apiroute.Tags("数据服务"), apiroute.Summary("更新数据服务"), apiroute.Perm("dataservice:write"), apiroute.WithReqBody(dataservice.DataService{}), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("DELETE", "/api/dataservices/{id}", apiroute.Tags("数据服务"), apiroute.Summary("删除数据服务"), apiroute.Perm("dataservice:write"))
	reg.Operation("POST", "/api/dataservices/{id}/start", apiroute.Tags("数据服务"), apiroute.Summary("启动实例（replicas→1）"), apiroute.Perm("dataservice:write"), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("POST", "/api/dataservices/{id}/stop", apiroute.Tags("数据服务"), apiroute.Summary("停止实例（replicas→0，省资源）"), apiroute.Perm("dataservice:write"), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("POST", "/api/dataservices/{id}/restart", apiroute.Tags("数据服务"), apiroute.Summary("滚动重启实例"), apiroute.Perm("dataservice:write"))
	reg.Operation("PUT", "/api/dataservices/{id}/scale", apiroute.Tags("数据服务"), apiroute.Summary("扩缩容（replicas/cpu/memory/storageGb）"), apiroute.Perm("dataservice:write"), apiroute.WithResp(dataservice.DataService{}))
	reg.Operation("PUT", "/api/dataservices/{id}/upgrade", apiroute.Tags("数据服务"), apiroute.Summary("版本升级（image）"), apiroute.Perm("dataservice:write"), apiroute.WithResp(dataservice.DataService{}))
	// 引擎目录（平台级 admin 配置 + 用户 enabled 列表）
	reg.Operation("GET", "/api/engines", apiroute.Tags("引擎目录"), apiroute.Summary("enabled 引擎列表（创建表单）"), apiroute.WithResp([]dataservice.Engine{}))
	reg.Operation("GET", "/api/admin/engines", apiroute.Tags("引擎目录"), apiroute.Summary("全部引擎列表（admin）"), apiroute.Perm("super_admin"), apiroute.WithResp([]dataservice.Engine{}))
	reg.Operation("POST", "/api/admin/engines", apiroute.Tags("引擎目录"), apiroute.Summary("创建引擎"), apiroute.Perm("super_admin"), apiroute.WithReqBody(dataservice.Engine{}), apiroute.WithResp(dataservice.Engine{}))
	reg.Operation("PUT", "/api/admin/engines/{id}", apiroute.Tags("引擎目录"), apiroute.Summary("更新引擎"), apiroute.Perm("super_admin"), apiroute.WithReqBody(dataservice.Engine{}), apiroute.WithResp(dataservice.Engine{}))
	reg.Operation("DELETE", "/api/admin/engines/{id}", apiroute.Tags("引擎目录"), apiroute.Summary("删除引擎"), apiroute.Perm("super_admin"))
	// 推理（流式）
	reg.Operation("POST", "/v1/chat/completions", apiroute.Tags("MaaS"), apiroute.Summary("流式推理（OpenAI 兼容 SSE；model 支持 agent:{id} 虚拟模型调 Agent）"), apiroute.Perm("model:infer"), apiroute.WithReqBody(provider.ChatRequest{}))

	// /api/auth/* + /api/system/menus：console-admin 身份对接。
	// login/refresh 公开（不挂 BearerAuth）；logout/me/menus 需鉴权。
	reg.Register("POST", "/api/auth/sessions", http.HandlerFunc(authHandler.Login),
		apiroute.Tags("认证"), apiroute.Summary("登录（用户名密码换 token）"),
		apiroute.WithReqBody(authPkg.LoginRequest{}), apiroute.WithResp(authPkg.AuthResult{}))
	reg.Register("POST", "/api/auth/tokens/refresh", http.HandlerFunc(authHandler.Refresh),
		apiroute.Tags("认证"), apiroute.Summary("刷新 token"),
		apiroute.WithReqBody(authPkg.RefreshRequest{}), apiroute.WithResp(authPkg.AuthResult{}))
	reg.Register("DELETE", "/api/auth/sessions", auth(http.HandlerFunc(authHandler.Logout)),
		apiroute.Tags("认证"), apiroute.Summary("登出（无状态，前端清 token）"))
	reg.Register("GET", "/api/auth/users/me", auth(http.HandlerFunc(authHandler.Me)),
		apiroute.Tags("认证"), apiroute.Summary("当前用户信息"),
		apiroute.WithResp(authPkg.UserProfile{}))
	reg.Register("GET", "/api/system/menus", auth(http.HandlerFunc(authHandler.Menus)),
		apiroute.Tags("认证"), apiroute.Summary("菜单下发（动态路由装载）"),
		apiroute.WithResp([]authPkg.Menu{}))

	// dashboard 聚合统计（admin 后台首页；/api/admin/* 平台运维域，super_admin 通行）。
	reg.Register("GET", "/api/admin/dashboard/stats", adminGuard(http.HandlerFunc(dashHandler.Stats)),
		apiroute.Tags("平台总览"), apiroute.Summary("首页统计聚合"), apiroute.WithResp(dashboard.Stats{}))
	reg.Register("GET", "/api/admin/dashboard/charts", adminGuard(http.HandlerFunc(dashHandler.Charts)),
		apiroute.Tags("平台总览"), apiroute.Summary("趋势与分布"), apiroute.WithResp(dashboard.Charts{}))
	reg.Register("GET", "/api/admin/dashboard/activities", adminGuard(http.HandlerFunc(dashHandler.Activities)),
		apiroute.Tags("平台总览"), apiroute.Summary("动态"), apiroute.WithResp([]dashboard.Activity{}))

	// 跨租户资源总览（super_admin）：列出全部租户的应用/工作负载/数据服务，供 console-admin 资源总览消费。
	// 仅读：跨租户写越权风险高，资源运维仍在 console-user 租户内进行（admin 总览用于观测/排查）。
	renderList := func(w http.ResponseWriter, list any, err error) {
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
	}
	// admin 工作负载管理（详情+实例+日志 / 扩缩容 / 删除）：mux.Handle 到 wlAdminHandler（按 method+action 分发），
	// spec 经 reg.Operation 登记。handler 内 ServeHTTP 已含 GET 列表 + GET/{id} 详情 + GET/{id}/logs + PUT/{id}/scale + DELETE/{id}。
	mux.Handle("/api/admin/workloads", adminGuard(wlAdminHandler))
	mux.Handle("/api/admin/workloads/", adminGuard(wlAdminHandler))
	reg.Operation("GET", "/api/admin/workloads", apiroute.Tags("工作负载管理"), apiroute.Summary("工作负载列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]workload.Workload{}))
	reg.Operation("GET", "/api/admin/workloads/{id}", apiroute.Tags("工作负载管理"), apiroute.Summary("工作负载详情（跨租户，含实例）"), apiroute.Perm("super_admin"))
	reg.Operation("GET", "/api/admin/workloads/{id}/logs", apiroute.Tags("工作负载管理"), apiroute.Summary("实例日志（Pod 级）"), apiroute.Perm("super_admin"))
	reg.Operation("PUT", "/api/admin/workloads/{id}/scale", apiroute.Tags("工作负载管理"), apiroute.Summary("扩缩容（绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Operation("DELETE", "/api/admin/workloads/{id}", apiroute.Tags("工作负载管理"), apiroute.Summary("强制删除（回收配额）"), apiroute.Perm("super_admin"))
	// admin 应用管理（详情 / 删除）：mux.Handle 到 appAdminHandler（按 method 分发）。不代建（业务编排类）。
	mux.Handle("/api/admin/applications", adminGuard(appAdminHandler))
	mux.Handle("/api/admin/applications/", adminGuard(appAdminHandler))
	reg.Operation("GET", "/api/admin/applications", apiroute.Tags("应用管理"), apiroute.Summary("应用列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]application.Application{}))
	reg.Operation("GET", "/api/admin/applications/{id}", apiroute.Tags("应用管理"), apiroute.Summary("应用详情（跨租户）"), apiroute.Perm("super_admin"))
	reg.Operation("DELETE", "/api/admin/applications/{id}", apiroute.Tags("应用管理"), apiroute.Summary("强制删除（级联清理 + 回收配额）"), apiroute.Perm("super_admin"))
	// admin 数据服务管理（详情+实例 / 运维 / 代建）：mux.Handle 到 dsAdminHandler（按 method+action 分发），
	// spec 经 reg.Operation 登记。handler 内 ServeHTTP 已含 GET 列表（掩码 Connection）+ POST 代建 + {id}/{action}。
	mux.Handle("/api/admin/dataservices", adminGuard(dsAdminHandler))
	mux.Handle("/api/admin/dataservices/", adminGuard(dsAdminHandler))
	reg.Operation("GET", "/api/admin/dataservices", apiroute.Tags("数据服务管理"), apiroute.Summary("数据服务列表（跨租户，掩码）"), apiroute.Perm("super_admin"), apiroute.WithResp([]dataservice.DataService{}))
	reg.Operation("POST", "/api/admin/dataservices", apiroute.Tags("数据服务管理"), apiroute.Summary("代建数据服务（指定租户，消耗配额）"), apiroute.Perm("super_admin"))
	reg.Operation("GET", "/api/admin/dataservices/{id}", apiroute.Tags("数据服务管理"), apiroute.Summary("数据服务详情（跨租户，含实例）"), apiroute.Perm("super_admin"))
	reg.Operation("DELETE", "/api/admin/dataservices/{id}", apiroute.Tags("数据服务管理"), apiroute.Summary("强制删除（回收配额）"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/stop", apiroute.Tags("数据服务管理"), apiroute.Summary("停止"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/start", apiroute.Tags("数据服务管理"), apiroute.Summary("启动"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/restart", apiroute.Tags("数据服务管理"), apiroute.Summary("滚动重启"), apiroute.Perm("super_admin"))
	reg.Operation("PUT", "/api/admin/dataservices/{id}/scale", apiroute.Tags("数据服务管理"), apiroute.Summary("扩缩容"), apiroute.Perm("super_admin"))

	// 跨租户资源总览扩展（super_admin）：环境/DevOps/配置中心/治理/可观测/安全/计费。
	// 仅读：跨租户写越权风险高，资源运维仍在 console-user 租户内进行（admin 总览用于观测/排查）。
	// admin 环境管理（L1 详情 / L3 代建）：mux.Handle 到 envAdminHandler（GET 列表 + GET {id} 详情 + POST 代建）。
	// 双注册（无尾斜杠 + 有尾斜杠）兼容客户端追加尾斜杠访问，与 dataservice/workload/application 对齐。
	mux.Handle("/api/admin/environments", adminGuard(envAdminHandler))
	mux.Handle("/api/admin/environments/", adminGuard(envAdminHandler))
	reg.Operation("GET", "/api/admin/environments", apiroute.Tags("环境管理"), apiroute.Summary("环境列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]environment.Environment{}))
	reg.Operation("GET", "/api/admin/environments/{id}", apiroute.Tags("环境管理"), apiroute.Summary("环境详情（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp(environment.Environment{}))
	reg.Operation("POST", "/api/admin/environments", apiroute.Tags("环境管理"), apiroute.Summary("代建环境（指定租户）"), apiroute.Perm("super_admin"))
	reg.Register("GET", "/api/admin/buildruns", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.DevOpsBuilds.ListAllBuildRuns(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("构建列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]devops.BuildRun{}))
	reg.Register("GET", "/api/admin/images", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.DevOpsImages.ListAllImages(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("镜像列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]devops.Image{}))
	reg.Register("GET", "/api/admin/releases", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.DevOpsReleases.ListAllReleases(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("发布列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]devops.Release{}))
	// admin devops 管理（构建/镜像/发布 详情 + 发布回滚）：mux.Handle 到 devopsAdminHandler（按 path 前缀分发）。
	// /api/admin/buildruns|images|releases（无尾斜杠）GET 列表仍走上面 reg.Register；
	// /api/admin/buildruns|images|releases/（有尾斜杠，{id}）走 devopsAdminHandler。Go 1.22 ServeMux 最长前缀匹配区分两者。
	mux.Handle("/api/admin/buildruns/", adminGuard(devopsAdminHandler))
	mux.Handle("/api/admin/images/", adminGuard(devopsAdminHandler))
	mux.Handle("/api/admin/releases/", adminGuard(devopsAdminHandler))
	reg.Operation("GET", "/api/admin/buildruns/{id}", apiroute.Tags("DevOps管理"), apiroute.Summary("构建详情（跨租户，含日志）"), apiroute.Perm("super_admin"), apiroute.WithResp(devops.BuildRun{}))
	reg.Operation("GET", "/api/admin/images/{id}", apiroute.Tags("DevOps管理"), apiroute.Summary("镜像详情（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp(devops.Image{}))
	reg.Operation("GET", "/api/admin/releases/{id}", apiroute.Tags("DevOps管理"), apiroute.Summary("发布详情（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp(devops.Release{}))
	reg.Operation("POST", "/api/admin/releases/{id}/rollback", apiroute.Tags("DevOps管理"), apiroute.Summary("回滚发布（绕过 prod:write，记审计）"), apiroute.Perm("super_admin"), apiroute.WithResp(devops.Release{}))
	reg.Register("GET", "/api/admin/namespaces", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.ConfigCenter.ListAllNamespaces(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("配置命名空间列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]configcenter.Namespace{}))
	reg.Register("GET", "/api/admin/services", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.Governance.ListAllServices(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("服务列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]governance.Service{}))
	// admin governance 管理（服务详情+实例 / 注销实例·删服务）：mux.Handle 到 govAdminHandler（按 path 分发）。
	// /api/admin/services（无尾斜杠）GET 列表仍走 reg.Register；/api/admin/services/（有尾斜杠，{id}）走 govAdminHandler。
	mux.Handle("/api/admin/services/", adminGuard(govAdminHandler))
	reg.Operation("GET", "/api/admin/services/{id}", apiroute.Tags("治理管理"), apiroute.Summary("服务详情（跨租户，含实例）"), apiroute.Perm("super_admin"), apiroute.WithResp(governance.ServiceDetail{}))
	reg.Operation("DELETE", "/api/admin/services/{id}", apiroute.Tags("治理管理"), apiroute.Summary("强制删服务（级联清实例，绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Operation("DELETE", "/api/admin/services/{id}/instances/{iid}", apiroute.Tags("治理管理"), apiroute.Summary("注销实例（绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Register("GET", "/api/admin/alert-rules", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := obsRepo.ListAllAlertRules(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("告警规则列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]observability.AlertRule{}))
	// admin observability 管理（告警规则详情 / 删除）：mux.Handle 到 obsAdminHandler。
	// /api/admin/alert-rules（无尾斜杠）GET 列表仍走 reg.Register；/api/admin/alert-rules/（有尾斜杠，{id}）走 obsAdminHandler。
	mux.Handle("/api/admin/alert-rules/", adminGuard(obsAdminHandler))
	reg.Operation("GET", "/api/admin/alert-rules/{id}", apiroute.Tags("可观测管理"), apiroute.Summary("告警规则详情（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp(observability.AlertRule{}))
	reg.Operation("DELETE", "/api/admin/alert-rules/{id}", apiroute.Tags("可观测管理"), apiroute.Summary("强制删除告警规则（绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Register("GET", "/api/admin/secrets", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.Security.ListAllSecrets(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("密钥列表（跨租户，掩码）"), apiroute.Perm("super_admin"), apiroute.WithResp([]security.Secret{}))
	// admin security 管理（密钥详情（掩码）/ 删除）：mux.Handle 到 secAdminHandler。
	// /api/admin/secrets（无尾斜杠）GET 列表仍走 reg.Register；/api/admin/secrets/（有尾斜杠，{id}）走 secAdminHandler。
	mux.Handle("/api/admin/secrets/", adminGuard(secAdminHandler))
	reg.Operation("GET", "/api/admin/secrets/{id}", apiroute.Tags("安全管理"), apiroute.Summary("密钥详情（跨租户，掩码）"), apiroute.Perm("super_admin"), apiroute.WithResp(security.Secret{}))
	reg.Operation("DELETE", "/api/admin/secrets/{id}", apiroute.Tags("安全管理"), apiroute.Summary("强制删除密钥（绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Register("GET", "/api/admin/audit-logs", adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := stores.Security.ListAllAuditLogs(r.Context())
		renderList(w, list, err)
	})),
		apiroute.Tags("资源总览"), apiroute.Summary("审计日志列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]security.AuditLog{}))
	// admin billing 管理（配额列表+调整 / 账单列表+详情+标记已付）：mux.Handle 到 billAdminHandler（按 method+path 分发）。
	// PUT /quotas 与 GET /quotas 同路径需同 handler 分发，原 reg.Register GET 列表删并入 billAdminHandler.ServeHTTP。
	mux.Handle("/api/admin/quotas", adminGuard(billAdminHandler))
	mux.Handle("/api/admin/quotas/", adminGuard(billAdminHandler))
	mux.Handle("/api/admin/bills", adminGuard(billAdminHandler))
	mux.Handle("/api/admin/bills/", adminGuard(billAdminHandler))
	reg.Operation("GET", "/api/admin/quotas", apiroute.Tags("计费管理"), apiroute.Summary("配额列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]billing.ResourceQuota{}))
	reg.Operation("PUT", "/api/admin/quotas", apiroute.Tags("计费管理"), apiroute.Summary("调整配额（绕过 prod:write）"), apiroute.Perm("super_admin"))
	reg.Operation("GET", "/api/admin/bills", apiroute.Tags("计费管理"), apiroute.Summary("账单列表（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp([]billing.BillingRecord{}))
	reg.Operation("GET", "/api/admin/bills/{id}", apiroute.Tags("计费管理"), apiroute.Summary("账单详情（跨租户）"), apiroute.Perm("super_admin"), apiroute.WithResp(billing.BillingRecord{}))
	reg.Operation("POST", "/api/admin/bills/{id}/pay", apiroute.Tags("计费管理"), apiroute.Summary("标记账单已付（绕过 prod:write）"), apiroute.Perm("super_admin"), apiroute.WithResp(billing.BillingRecord{}))

	// messaging（MQ topic/消费组 CRUD，租户隔离，方法级 dataservice 权限）。
	mux.Handle("/api/mq-topics", auth(msgHandler))
	mux.Handle("/api/mq-topics/", auth(msgHandler))
	mux.Handle("/api/consumer-groups", auth(msgHandler))
	mux.Handle("/api/consumer-groups/", auth(msgHandler))
	reg.Operation("GET", "/api/mq-topics", apiroute.Tags("消息队列"), apiroute.Summary("Topic 列表（?mqId=）"), apiroute.Perm("dataservice:read"), apiroute.WithResp([]messaging.Topic{}))
	reg.Operation("POST", "/api/mq-topics", apiroute.Tags("消息队列"), apiroute.Summary("创建 Topic"), apiroute.Perm("dataservice:write"), apiroute.WithReqBody(messaging.Topic{}), apiroute.WithResp(messaging.Topic{}))
	reg.Operation("DELETE", "/api/mq-topics/{id}", apiroute.Tags("消息队列"), apiroute.Summary("删除 Topic（级联清消费组）"), apiroute.Perm("dataservice:write"))
	reg.Operation("GET", "/api/consumer-groups", apiroute.Tags("消息队列"), apiroute.Summary("消费组列表（?topicId=）"), apiroute.Perm("dataservice:read"), apiroute.WithResp([]messaging.ConsumerGroup{}))
	reg.Operation("POST", "/api/consumer-groups", apiroute.Tags("消息队列"), apiroute.Summary("创建消费组"), apiroute.Perm("dataservice:write"), apiroute.WithReqBody(messaging.ConsumerGroup{}), apiroute.WithResp(messaging.ConsumerGroup{}))
	reg.Operation("DELETE", "/api/consumer-groups/{id}", apiroute.Tags("消息队列"), apiroute.Summary("删除消费组"), apiroute.Perm("dataservice:write"))

	// backup（数据服务备份 CRUD）。
	mux.Handle("/api/backups", auth(bkHandler))
	mux.Handle("/api/backups/", auth(bkHandler))
	reg.Operation("GET", "/api/backups", apiroute.Tags("数据服务"), apiroute.Summary("备份列表（?resourceId=）"), apiroute.Perm("dataservice:read"), apiroute.WithResp([]backup.Backup{}))
	reg.Operation("POST", "/api/backups", apiroute.Tags("数据服务"), apiroute.Summary("创建备份"), apiroute.Perm("dataservice:write"), apiroute.WithReqBody(backup.Backup{}), apiroute.WithResp(backup.Backup{}))
	reg.Operation("DELETE", "/api/backups/{id}", apiroute.Tags("数据服务"), apiroute.Summary("删除备份"), apiroute.Perm("dataservice:write"))

	// 数据面 SDK 接入（/dp/，zeus paas-registry 插件消费；mux 见 mux.Handle("/dp/")，BearerAuth 鉴权）。
	reg.Operation("GET", "/dp/services", apiroute.Tags("数据面"), apiroute.Summary("列服务（Discovery）"), apiroute.Perm("dp:read"))
	reg.Operation("GET", "/dp/instances", apiroute.Tags("数据面"), apiroute.Summary("列实例（?service=，从 K8s Endpoints 读）"), apiroute.Perm("dp:read"))
	reg.Operation("POST", "/dp/register", apiroute.Tags("数据面"), apiroute.Summary("声明服务元信息（幂等）"), apiroute.Perm("dp:write"), apiroute.WithReqBody(dataplane.ServiceInfo{}))
	reg.Operation("DELETE", "/dp/register", apiroute.Tags("数据面"), apiroute.Summary("反注册服务（?id=）"), apiroute.Perm("dp:write"))
	reg.Operation("PUT", "/dp/heartbeat", apiroute.Tags("数据面"), apiroute.Summary("心跳（兼容保留，K8s readiness 是真源）"), apiroute.Perm("dp:write"))

	// identity 管理 API（平台运维域 /api/admin/*，super_admin 通行）。
	reg.Register("GET", "/api/admin/tenants", adminGuard(http.HandlerFunc(idmHandler.ListTenants)),
		apiroute.Tags("身份管理"), apiroute.Summary("租户列表"), apiroute.WithResp([]identity.Tenant{}))
	reg.Register("POST", "/api/admin/tenants", adminGuard(http.HandlerFunc(idmHandler.CreateTenant)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建租户"), apiroute.WithReqBody(identity.Tenant{}), apiroute.WithResp(identity.Tenant{}))
	reg.Register("DELETE", "/api/admin/tenants/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteTenant)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除租户（级联）"))
	reg.Register("GET", "/api/admin/users", adminGuard(http.HandlerFunc(idmHandler.ListUsers)),
		apiroute.Tags("身份管理"), apiroute.Summary("用户列表（?tenantId= 过滤）"), apiroute.WithResp([]identity.User{}))
	reg.Register("POST", "/api/admin/users", adminGuard(http.HandlerFunc(idmHandler.CreateUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建用户（含密码）"), apiroute.WithReqBody(identity.User{}), apiroute.WithResp(identity.User{}))
	reg.Register("PUT", "/api/admin/users/{id}", adminGuard(http.HandlerFunc(idmHandler.UpdateUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("更新用户（roles/status/密码可选）"), apiroute.WithResp(identity.User{}))
	reg.Register("DELETE", "/api/admin/users/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除用户"))
	reg.Register("GET", "/api/admin/api-keys", adminGuard(http.HandlerFunc(idmHandler.ListAPIKeys)),
		apiroute.Tags("身份管理"), apiroute.Summary("API Key 列表（掩码，?tenantId= 过滤）"), apiroute.WithResp([]identity.APIKey{}))
	reg.Register("POST", "/api/admin/api-keys", adminGuard(http.HandlerFunc(idmHandler.CreateAPIKey)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建 API Key（返明文一次）"), apiroute.WithReqBody(identity.APIKey{}), apiroute.WithResp(identity.APIKey{}))
	reg.Register("DELETE", "/api/admin/api-keys/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteAPIKey)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除 API Key"))
	reg.Register("GET", "/api/admin/roles", adminGuard(http.HandlerFunc(idmHandler.ListRoles)),
		apiroute.Tags("身份管理"), apiroute.Summary("内置角色列表（只读）"))

	// API Key 自助端点（/api/api-keys，auth 守卫，任意已认证用户管理本租户密钥）。
	// 复用 idmHandler 方法：非超管分支强制本租户 + 绑定调用者用户 + roles 封顶（零提权），
	// super_admin 走 /api/admin/api-keys 跨租户视图。console-user ApiKeys.vue 消费此端点。
	reg.Register("GET", "/api/api-keys", auth(http.HandlerFunc(idmHandler.ListAPIKeys)),
		apiroute.Tags("API 密钥"), apiroute.Summary("本租户 API Key 列表（掩码）"), apiroute.WithResp([]identity.APIKey{}))
	reg.Register("POST", "/api/api-keys", auth(http.HandlerFunc(idmHandler.CreateAPIKey)),
		apiroute.Tags("API 密钥"), apiroute.Summary("创建本租户 API Key（返明文一次，roles 封顶自身）"), apiroute.WithReqBody(identity.APIKey{}), apiroute.WithResp(identity.APIKey{}))
	reg.Register("DELETE", "/api/api-keys/{id}", auth(http.HandlerFunc(idmHandler.DeleteAPIKey)),
		apiroute.Tags("API 密钥"), apiroute.Summary("删除本租户 API Key"))

	srv := &http.Server{
		Addr: ":8080",
		// recovery 中间件包 mux 最内层（捕获 handler panic，防单请求挂掉进程，SSE 流式也保护）；
		// csrf 在其外（写操作 Origin/Referer 同源校验，cookie 会话防 CSRF 纵深）；
		// otelhttp 再包外层（自动建 span，过滤探针/契约/文档端点避免噪音）。
		Handler: securityHeadersMiddleware(otelhttp.NewHandler(recoveryMiddleware(csrfMiddleware(mux)), "http.server",
			otelhttp.WithFilter(skipTelemetryPaths))),
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris 慢速头部攻击
	}
	// 仅打印 Key 前缀，避免生产 API Key 明文进容器日志/日志聚合系统（运维确认用长度 + 前 6 字符）。
	if apiKey != "" {
		prefix := apiKey
		if len(prefix) > 6 {
			prefix = prefix[:6]
		}
		log.Printf("HTTP 监听 :8080（API Key: %s***，len=%d）", prefix, len(apiKey))
	} else {
		log.Printf("HTTP 监听 :8080（无默认 API Key）")
	}
	// 后台监听；run() 在收到 SIGTERM 后调 srv.Shutdown 优雅关闭（in-flight 请求/SSE 流式有 grace 期）。
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP 服务异常退出: %v", err)
		}
	}()
	return srv
}

// skipTelemetryPaths 过滤探针/契约/文档端点，避免高频无业务语义请求污染链路。
func skipTelemetryPaths(r *http.Request) bool {
	switch r.URL.Path {
	case "/livez", "/openapi.json", "/docs":
		return false // 跳过（不建 span）
	}
	return true
}

// recoveryMiddleware 捕获 handler panic，防止单请求 panic（json.Decode 异常类型、nil map 写、
// 越界等）挂掉整个进程（in-flight 请求/SSE 流被强断）。panic 栈入服务端日志，客户端只收 500 internal error。
func recoveryMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[panic] %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack()) //nolint:gosec // 请求 method/path 入日志是标准实践，非注入攻击面
				httputil.WriteError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		h.ServeHTTP(w, r)
	})
}
