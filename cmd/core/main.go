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
	"strings"
	"syscall"
	"time"

	"github.com/aitoys/paas/internal/apiroute"
	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/backup"
	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/dashboard"
	authPkg "github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/core/health"
	"github.com/aitoys/paas/internal/core/identity"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/internal/messaging"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/internal/observability/tracing"
	"github.com/aitoys/paas/internal/security"
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
	// 本切片注册 MaaS 插件（真实第三方供应商通道 + mock 演示）
	plugins := []plugin.Plugin{&maas.MaaSPlugin{}}

	if err := run(ctx, plugins, gw, meter); err != nil {
		log.Fatalf("core 启动失败: %v", err)
	}
}

// run 启动 HTTP 服务并引导插件；返回非 nil 则进程以 1 退出。
func run(ctx context.Context, plugins []plugin.Plugin, gw *gateway.Gateway, meter *gateway.Meter) error {
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
	// CredentialResolver 桥接 security store：注入 MaaS 插件解析平台级凭证。
	// security.SecretStore 接口含 Resolve（PG/memory 透明）。
	deps := realCoreDeps{gw: gw, resolver: secretResolver{store: stores.Security.(security.SecretStore)}}
	go serveHTTP(gw, meter, stores)
	ran, err := bootstrapCore(ctx, plugins, deps)
	if err != nil {
		return err
	}
	log.Printf("core 启动完成，已运行插件: %v", ran)
	<-ctx.Done()
	log.Println("core 收到退出信号，停止")
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

// resolveAPIKey 解析 API Key：环境变量 PAAS_API_KEY 优先，否则用开发默认值（sk-acme-admin）。
// 返回值用于 seed 兼容（自定义 Key 追加为 t-acme admin）与日志展示。
func resolveAPIKey() string {
	if k := os.Getenv("PAAS_API_KEY"); k != "" {
		return k
	}
	return "sk-acme-admin"
}

// resolveJWTSecret 解析 JWT 签名密钥。
// PAAS_JWT_SECRET 非空则用之；空则随机生成 32 字节（重启后旧 token 失效，生产请配置）。
func resolveJWTSecret() string {
	if s := os.Getenv("PAAS_JWT_SECRET"); s != "" {
		return s
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("生成 JWT 随机密钥失败: %v", err)
	}
	log.Printf("[auth] PAAS_JWT_SECRET 未配置，已随机生成（重启后旧 token 失效，生产环境请配置）")
	return base64.StdEncoding.EncodeToString(b)
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
func serveHTTP(gw *gateway.Gateway, meter *gateway.Meter, stores *Stores) {
	apiKey := resolveAPIKey()
	// P3-2 计量采集：推理 token 用量回写 billing（meter.OnTokens 钩子，IncUsage 按 tenant 计）。
	meter.OnTokens = func(tenantID string, tokens int) {
		if tenantID != "" {
			ctx := tenant.WithTenant(context.Background(), tenantID)
			_, _ = stores.Billing.IncUsage(ctx, billing.ResTokens, tokens)
		}
	}
	jwtSecret := resolveJWTSecret()
	mux := http.NewServeMux()
	// BearerAuth 双通道：JWT（admin 浏览器登录）/ API Key（程序化调用）共存，下游零改动。
	auth := gateway.BearerAuth(stores.Identity, jwtSecret)
	authHandler := authPkg.NewHandler(stores.Identity, jwtSecret)
	// identity 管理 API（/api/tenants、/api/users、/api/api-keys、/api/roles）：平台级，需 tenant:admin。
	idmHandler := identity.NewHandler(stores.Identity).
		HashPassword(authPkg.HashPassword)
	// 注入平台超管判定：IsAdmin 用户 token 携带 super_admin 标记（auth.issueTokens），
	// 据此区分跨租户平台管理与本租户 tenant-admin，防越权。
	idmHandler.IsPlatformAdmin = gateway.IsPlatformAdmin
	adminGuard := func(h http.Handler) http.Handler {
		return auth(gateway.Require(identity.Permission("tenant:admin"))(h))
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
	mux.Handle("/v1/chat/completions", auth(gateway.Require("model:infer")(gateway.ChatCompletions(gw, meter))))
	// 模型目录端点（GET-only）走 Register：验证 mux 驱动 + spec 生成双路径。
	reg.Register("GET", "/v1/models", auth(gateway.Require("model:read")(gateway.ListModels(gw))),
		apiroute.Tags("MaaS"), apiroute.Summary("OpenAI 兼容模型列表"),
		apiroute.Perm("model:read"), apiroute.WithResp(nil))
	reg.Register("GET", "/api/models", auth(gateway.Require("model:read")(gateway.CatalogModels(gw))),
		apiroute.Tags("MaaS"), apiroute.Summary("模型市场富信息（含通道）"),
		apiroute.Perm("model:read"), apiroute.WithResp(nil))
	// 应用为主线 REST API：方法级权限由 handler 内部按 application:*/binding:write 校验
	// 横切配额拦截：创建应用前调 billing.CheckAndInc，超限回 429（stores.Billing 共享用量真源）。
	appHandler := application.NewHandler(stores.Application)
	appHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	appHandler.QuotaCheck = func(ctx context.Context, delta int) error {
		_, err := stores.Billing.CheckAndInc(ctx, billing.ResApplications, delta)
		return err
	}

	// 环境（物理隔离单元 prod|test）：方法级权限 environment:read/write + prod 写校验。
	// 同时作 EnvTypeResolver 横切注入到 workload/devops/appconfig/governance/dataservice。
	envHandler := environment.NewHandler(stores.Environment)
	envHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/environments", auth(envHandler))
	mux.Handle("/api/environments/", auth(envHandler))

	// 工作负载：注入 environment store 作 EnvTypeResolver，启用生产写校验（dev 生产只读）。
	// stores.Workload 与 DevOps 共享：Release 编排要更新 Workload.ImageRef。
	wlHandler := workload.NewHandler(stores.Workload, workload.WithEnvResolver(stores.Environment))
	wlHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	// 横切配额拦截：创建工作负载前调 billing.CheckAndInc，超限回 429。
	wlHandler.QuotaCheck = func(ctx context.Context, delta int) error {
		_, err := stores.Billing.CheckAndInc(ctx, billing.ResWorkloads, delta)
		return err
	}

	// DevOps（代码->构建->镜像->发布->回滚）：注入 environment store（prod 写校验）+ UserIDFrom（填发布人）。
	// stores.DevOps* 四子接口由同一 store 实现（内存/PG 同构）；Release 编排经 workload 仓储接口透明。
	devopsHandler := devops.NewHandler(stores.DevOpsRepos, stores.DevOpsBuilds, stores.DevOpsImages, stores.DevOpsReleases,
		devops.WithEnvResolver(stores.Environment),
		devops.WithUserIDFrom(gateway.UserIDFrom),
	)
	devopsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 应用配置（工作负载级 env/Secret）：注入 environment store（prod 写校验）。
	// Secret 后端明文存储，API 返回固定掩码；与配置中心（服务治理，运行时动态）严格区分。
	appconfigHandler := appconfig.NewHandler(stores.AppConfig, appconfig.WithEnvResolver(stores.Environment))
	appconfigHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 服务治理（注册中心 = 平台能力横切）：注入 environment store（prod 写校验）。
	// 服务/实例租户私有；本期进程内 mock，数据面 SDK 接入留后续。
	govHandler := governance.NewHandler(stores.Governance, governance.WithEnvResolver(stores.Environment))
	govHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 配置中心（治理四件套：运行时动态配置，版本/发布/回滚）。
	// 独立于物理环境（namespace 逻辑隔离），不接 EnvTypeResolver；复用 governance:read/write 权限。
	ccHandler := configcenter.NewHandler(stores.ConfigCenter)
	ccHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 可观测（指标监控 + 告警规则，平台能力横切）。
	// 惰性时序模拟采集，即时评估告警；不接 prod:write，独立于物理环境。
	obsHandler := observability.NewHandler(buildObservabilityStore())
	obsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 安全（密钥/证书 + 审计日志，平台能力横切）。
	// 注入 UserIDFrom 填审计 actor；Secret 明文存储/掩码返回，写操作自动记审计。
	// 平台级 Secret（如第三方供应商凭证）仅 tenant-admin 可写。stores.Security 同时桥接为 CredentialResolver。
	secHandler := security.NewHandler(stores.Security, security.WithUserIDFrom(gateway.UserIDFrom), security.WithIsAdmin(gateway.IsAdmin))
	secHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 配额计费（租户级资源配额 + 用量 + 账单，多租户商业化根基）。
	// 独立于物理环境，不接 prod:write；权限 billing:read/write（admin 写，dev/view 读）。
	// stores.Billing 与资源 Create 配额拦截共享同一 Store（用量真源唯一）。
	billingHandler := billing.NewHandler(stores.Billing)
	billingHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 数据服务（资源中心：DB/缓存/MQ/存储/向量/搜索，通用领域 + Kind 区分）。
	// 注入 environment store（prod 写校验）；权限 dataservice:read/write。
	dsHandler := dataservice.NewHandler(stores.DataService, dataservice.WithEnvResolver(stores.Environment))
	dsHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

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
	// 数据服务（资源中心 CRUD + meta）
	mux.Handle("/api/dataservices", auth(dsHandler))
	mux.Handle("/api/dataservices/", auth(dsHandler))

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

	// —— 其余模块 OpenAPI 元数据（Operation：spec-only，mux 注册见上方 mux.Handle）——
	// 工作负载（应用子资源 + 跨应用列表）
	reg.Operation("GET", "/api/applications/{id}/workloads", apiroute.Tags("工作负载"), apiroute.Summary("应用下工作负载"), apiroute.Perm("workload:read"), apiroute.WithResp([]workload.Workload{}))
	reg.Operation("POST", "/api/applications/{id}/workloads", apiroute.Tags("工作负载"), apiroute.Summary("创建工作负载"), apiroute.Perm("workload:write"), apiroute.WithReqBody(workload.Workload{}), apiroute.WithResp(workload.Workload{}))
	reg.Operation("GET", "/api/workloads", apiroute.Tags("工作负载"), apiroute.Summary("跨应用工作负载列表"), apiroute.Perm("workload:read"), apiroute.WithResp([]workload.Workload{}))
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
	reg.Operation("GET", "/api/images", apiroute.Tags("DevOps"), apiroute.Summary("跨应用镜像列表"), apiroute.Perm("image:read"), apiroute.WithResp([]devops.Image{}))
	reg.Operation("GET", "/api/releases", apiroute.Tags("DevOps"), apiroute.Summary("跨应用发布列表"), apiroute.Perm("release:read"), apiroute.WithResp([]devops.Release{}))
	reg.Operation("POST", "/api/releases/{id}/rollback", apiroute.Tags("DevOps"), apiroute.Summary("回滚发布"), apiroute.Perm("release:write"), apiroute.WithResp(devops.Release{}))
	// 应用配置
	reg.Operation("GET", "/api/applications/{id}/configs", apiroute.Tags("应用配置"), apiroute.Summary("应用配置项（掩码）"), apiroute.Perm("config:read"), apiroute.WithResp([]appconfig.ConfigItem{}))
	reg.Operation("POST", "/api/applications/{id}/configs", apiroute.Tags("应用配置"), apiroute.Summary("新增/更新配置项"), apiroute.Perm("config:write"), apiroute.WithReqBody(appconfig.ConfigItem{}), apiroute.WithResp(appconfig.ConfigItem{}))
	reg.Operation("DELETE", "/api/applications/{id}/configs/{cfgId}", apiroute.Tags("应用配置"), apiroute.Summary("删除配置项"), apiroute.Perm("config:write"))
	// 服务治理（注册中心 + API 网关路由 + 熔断）
	reg.Operation("GET", "/api/services", apiroute.Tags("服务治理"), apiroute.Summary("服务列表"), apiroute.Perm("governance:read"), apiroute.WithResp([]governance.Service{}))
	reg.Operation("POST", "/api/services", apiroute.Tags("服务治理"), apiroute.Summary("注册服务"), apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Service{}), apiroute.WithResp(governance.Service{}))
	reg.Operation("GET", "/api/services/{id}", apiroute.Tags("服务治理"), apiroute.Summary("服务详情"), apiroute.Perm("governance:read"), apiroute.WithResp(governance.Service{}))
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
	// 推理（流式）
	reg.Operation("POST", "/v1/chat/completions", apiroute.Tags("MaaS"), apiroute.Summary("流式推理（OpenAI 兼容 SSE）"), apiroute.Perm("model:infer"), apiroute.WithReqBody(provider.ChatRequest{}))

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

	// dashboard 聚合统计（admin 首页；需 tenant:admin）。
	reg.Register("GET", "/api/dashboard/stats", adminGuard(http.HandlerFunc(dashHandler.Stats)),
		apiroute.Tags("平台总览"), apiroute.Summary("首页统计聚合"), apiroute.WithResp(dashboard.Stats{}))
	reg.Register("GET", "/api/dashboard/charts", adminGuard(http.HandlerFunc(dashHandler.Charts)),
		apiroute.Tags("平台总览"), apiroute.Summary("趋势与分布"), apiroute.WithResp(dashboard.Charts{}))
	reg.Register("GET", "/api/dashboard/activities", adminGuard(http.HandlerFunc(dashHandler.Activities)),
		apiroute.Tags("平台总览"), apiroute.Summary("动态"), apiroute.WithResp([]dashboard.Activity{}))

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

	// identity 管理 API（平台级 CRUD，需 tenant:admin；super_admin 通行）。
	reg.Register("GET", "/api/tenants", adminGuard(http.HandlerFunc(idmHandler.ListTenants)),
		apiroute.Tags("身份管理"), apiroute.Summary("租户列表"), apiroute.WithResp([]identity.Tenant{}))
	reg.Register("POST", "/api/tenants", adminGuard(http.HandlerFunc(idmHandler.CreateTenant)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建租户"), apiroute.WithReqBody(identity.Tenant{}), apiroute.WithResp(identity.Tenant{}))
	reg.Register("DELETE", "/api/tenants/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteTenant)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除租户（级联）"))
	reg.Register("GET", "/api/users", adminGuard(http.HandlerFunc(idmHandler.ListUsers)),
		apiroute.Tags("身份管理"), apiroute.Summary("用户列表（?tenantId= 过滤）"), apiroute.WithResp([]identity.User{}))
	reg.Register("POST", "/api/users", adminGuard(http.HandlerFunc(idmHandler.CreateUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建用户（含密码）"), apiroute.WithResp(identity.User{}))
	reg.Register("PUT", "/api/users/{id}", adminGuard(http.HandlerFunc(idmHandler.UpdateUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("更新用户（roles/status/密码可选）"), apiroute.WithResp(identity.User{}))
	reg.Register("DELETE", "/api/users/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteUser)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除用户"))
	reg.Register("GET", "/api/api-keys", adminGuard(http.HandlerFunc(idmHandler.ListAPIKeys)),
		apiroute.Tags("身份管理"), apiroute.Summary("API Key 列表（掩码，?tenantId= 过滤）"), apiroute.WithResp([]identity.APIKey{}))
	reg.Register("POST", "/api/api-keys", adminGuard(http.HandlerFunc(idmHandler.CreateAPIKey)),
		apiroute.Tags("身份管理"), apiroute.Summary("创建 API Key（返明文一次）"), apiroute.WithResp(identity.APIKey{}))
	reg.Register("DELETE", "/api/api-keys/{id}", adminGuard(http.HandlerFunc(idmHandler.DeleteAPIKey)),
		apiroute.Tags("身份管理"), apiroute.Summary("删除 API Key"))
	reg.Register("GET", "/api/roles", adminGuard(http.HandlerFunc(idmHandler.ListRoles)),
		apiroute.Tags("身份管理"), apiroute.Summary("内置角色列表（只读）"))

	srv := &http.Server{
		Addr: ":8080",
		// otelhttp 包装 mux：自动为每个请求建 span（含 http.method/status_code/server.address）。
		// 过滤探针/契约/文档端点（高频无业务语义，避免噪音 span 淹没真实链路）。
		Handler:           otelhttp.NewHandler(mux, "http.server",
			otelhttp.WithFilter(skipTelemetryPaths)),
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris 慢速头部攻击
	}
	log.Printf("HTTP 监听 :8080（默认 API Key: %s）", apiKey)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("HTTP 服务退出: %v", err)
	}
}

// skipTelemetryPaths 过滤探针/契约/文档端点，避免高频无业务语义请求污染链路。
func skipTelemetryPaths(r *http.Request) bool {
	switch r.URL.Path {
	case "/livez", "/openapi.json", "/docs":
		return false // 跳过（不建 span）
	}
	return true
}
