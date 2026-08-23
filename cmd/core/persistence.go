// 持久化后端选择与 seed 编排。
//
// PAAS_DB_URL 非空 → PostgreSQL（迁移 + 表空才 seed，幂等）；为空 → 内存（与现状一致）。
// Repository 接口是切换点：PG 实现对 handler/路由/鉴权透明。
//
// buildAllStores 是后端切换唯一收口：构造全部 11 个模块 store + 跨模块依赖注入
// （devops PG 注入 workload 仓储）+ SeedIfEmpty 编排，返回 *Stores + closeFn。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/aitoys/paas/internal/ai/agent"
	agentmemory "github.com/aitoys/paas/internal/ai/agent/memory"
	agentpg "github.com/aitoys/paas/internal/ai/agent/pg"
	"github.com/aitoys/paas/internal/ai/eval"
	evalmemory "github.com/aitoys/paas/internal/ai/eval/memory"
	evalpg "github.com/aitoys/paas/internal/ai/eval/pg"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	kbmemory "github.com/aitoys/paas/internal/ai/knowledgebase/memory"
	kbpg "github.com/aitoys/paas/internal/ai/knowledgebase/pg"
	"github.com/aitoys/paas/internal/ai/prompt"
	promptmemory "github.com/aitoys/paas/internal/ai/prompt/memory"
	promptpg "github.com/aitoys/paas/internal/ai/prompt/pg"
	"github.com/aitoys/paas/internal/ai/tool"
	toolmemory "github.com/aitoys/paas/internal/ai/tool/memory"
	toolpg "github.com/aitoys/paas/internal/ai/tool/pg"
	"github.com/aitoys/paas/internal/appconfig"
	appcfgmemory "github.com/aitoys/paas/internal/appconfig/memory"
	appcfgpg "github.com/aitoys/paas/internal/appconfig/pg"
	"github.com/aitoys/paas/internal/backup"
	bkmemory "github.com/aitoys/paas/internal/backup/memory"
	"github.com/aitoys/paas/internal/billing"
	billingmemory "github.com/aitoys/paas/internal/billing/memory"
	billingpg "github.com/aitoys/paas/internal/billing/pg"
	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"
	ccpg "github.com/aitoys/paas/internal/configcenter/pg"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/core/application"
	appmemory "github.com/aitoys/paas/internal/core/application/memory"
	applicationpg "github.com/aitoys/paas/internal/core/application/pg"
	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
	identitypg "github.com/aitoys/paas/internal/core/identity/pg"
	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	dspg "github.com/aitoys/paas/internal/dataservice/pg"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/builder"
	"github.com/aitoys/paas/internal/devops/change"
	changepg "github.com/aitoys/paas/internal/devops/change/pg"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	devopspg "github.com/aitoys/paas/internal/devops/pg"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/environment"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	envpg "github.com/aitoys/paas/internal/environment/pg"
	"github.com/aitoys/paas/internal/observability"
	obspg "github.com/aitoys/paas/internal/observability/pg"
	"github.com/aitoys/paas/internal/governance"
	govmemory "github.com/aitoys/paas/internal/governance/memory"
	govpg "github.com/aitoys/paas/internal/governance/pg"
	"github.com/aitoys/paas/internal/maas"
	mapg "github.com/aitoys/paas/internal/maas/pg"
	"github.com/aitoys/paas/internal/messaging"
	msgmemory "github.com/aitoys/paas/internal/messaging/memory"
	"github.com/aitoys/paas/internal/security"
	secmemory "github.com/aitoys/paas/internal/security/memory"
	secpg "github.com/aitoys/paas/internal/security/pg"
	"github.com/aitoys/paas/internal/service"
	svcmemory "github.com/aitoys/paas/internal/service/memory"
	svcpg "github.com/aitoys/paas/internal/service/pg"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	wlpg "github.com/aitoys/paas/internal/workload/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// envNamespaceResolver 实现 dataservice.NamespaceResolver，按租户派生数据面 ns（paas-<tenant>）。
// 数据服务 FQDN 的 ns 随租户走，与 applier/reconciler 落地 ns 同源。
type envNamespaceResolver struct{}

func (envNamespaceResolver) Namespace(tid string) string { return tenant.Namespace(tid) }

// Stores 聚合全 11 模块 store，由 buildAllStores 构造（PG 或内存两路径统一形态）。
// 字段类型为各模块 Repository 接口；handler 注入点对后端透明。
// devops 是四子接口合集，无单一 Repository 接口，故拆 4 字段（同一 store 实例同时实现全部）。
type Stores struct {
	Identity       identity.Repository
	Application    application.Repository
	Environment    environment.Repository
	AppConfig      appconfig.Repository
	DataService    dataservice.Repository
	Engine         dataservice.EngineRepository
	Workload       workload.Repository
	DevOpsRepos    devops.CodeRepoRepository
	DevOpsBuilds   devops.BuildRunRepository
	DevOpsImages   devops.ImageRepository
	DevOpsReleases devops.ReleaseRepository
	Governance     governance.Repository
	ConfigCenter   configcenter.Repository
	Billing        billing.Repository
	Security       security.Repository
	Messaging      messaging.Repository
	Backup         backup.Repository
	MaaS           maas.Repository
	KnowledgeBase  knowledgebase.Repository
	Tool           tool.Repository
	Prompt         prompt.Repository
	Agent          agent.Repository
	Eval           eval.Repository
	Pipeline       pipeline.Store
	Change         change.Repository
	Service        service.Repository
	// AlertRules 是告警规则存储（R4-C1 持久化）。PG 路径非 nil（重启不丢）；
	// 内存路径 nil——observability 内部回退 memory 规则存储（含 dev seed）。
	AlertRules observability.RuleStore
}

// buildAllStores 选择持久化后端、构造全模块 store 并完成 seed。
// 返回 closeFn 用于释放连接池（内存路径返回 nil，调用方判空）。
//
// 横切依赖：devops store 依赖 workload.Repository（Release 编排找/建/更新 Workload），
// 两路径下注入的 workload store 与 wlHandler 共享同一实例（用量/编排真源唯一）。
func buildAllStores(ctx context.Context, appliers k8sAppliers) (*Stores, func(), error) {
	devopsPipeline := newDevOpsPipeline(appliers) // PAAS_DEVOPS_BUILDER 选 k8s/process/mock
	nsResolver := envNamespaceResolver{}          // 数据服务连接 FQDN 用，按租户派生 ns
	if dsn := os.Getenv("PAAS_DB_URL"); dsn != "" {
		db, err := storagepg.Open(ctx, dsn)
		if err != nil {
			return nil, nil, err
		}
		if err := storagepg.RunMigrations(ctx, db); err != nil {
			db.Close()
			return nil, nil, err
		}

		// 构造全 11 PG store（devops 注入 workload PG store，与 handler 共享）。
		idb := identitypg.NewStore(db)
		appRepo := applicationpg.NewStore(db)
		envRepo := envpg.NewStore(db)
		appcfgRepo := appcfgpg.NewStore(db)
		rawDs := dspg.NewStore(db, dspg.WithNamespaceResolver(nsResolver))
		var dsRepo dataservice.Repository = rawDs
		if appliers.dataservice != nil {
			dsRepo = dataservice.NewApplyRepo(dsRepo, appliers.dataservice) // K8s 启用：dataservice 写投影 CRD
		}
		rawWl := wlpg.NewStore(db)
		var wlRepo workload.Repository = rawWl
		if appliers.workload != nil {
			wlRepo = workload.NewApplyRepo(wlRepo, appliers.workload) // K8s 启用：workload 写操作投影 CRD 期望状态
		}
		svcRepo := svcpg.NewStore(db)
		devopsRepo := devopspg.NewStore(db, wlRepo, devopspg.WithServiceLookup(serviceLookupBridge{repos: svcRepo})) // Release 编排经 workload.Repository 接口透明
		devopsRepo.SetPipeline(devopsPipeline)      // PAAS_DEVOPS_REAL=true 接真实 git/docker
		govRepo := govpg.NewStore(db)
		// K8s 启用：governance Route 写投影聚合 Ingress（按 host 多 path，hermes/nginx 标准）。
		// applier 持裸 govRepo（RouteStore+ServiceStore），ApplyRepo 包装后给 handler（无循环引用）。
		var govRepoWithApply governance.Repository = govRepo
		if appliers.client != nil {
			routeApplier := controller.NewK8sRouteApplier(appliers.client, govRepo, govRepo, ingressClassFromEnv())
			govRepoWithApply = governance.NewApplyRepo(govRepo, routeApplier)
		}
		ccRepo := ccpg.NewStore(db)
		billingRepo := billingpg.NewStore(db)
		secRepo := secpg.NewStore(db)
		maasRepo := mapg.NewStore(db)
		kbRepo := kbpg.NewStore(db)
		toolRepo := toolpg.NewStore(db)
		promptRepo := promptpg.NewStore(db)
		agentRepo := agentpg.NewStore(db)
		evalRepo := evalpg.NewStore(db)
		msgRepo := messaging.Repository(msgmemory.NewStore())
		bkRepo := backup.Repository(bkmemory.NewStore())
		pipelineStore := pipeline.NewPGStore(db.Pool())
		changeRepo := changepg.NewStore(db)

		seedPGAllIfEmpty(ctx, idb, appRepo, envRepo, appcfgRepo, rawDs, rawWl,
			devopsRepo, govRepo, ccRepo, billingRepo, secRepo)
		seedMaasCatalog(ctx, maasRepo, secRepo)
		seedEnginesIfEmpty(ctx, rawDs)
		// 平台预置流水线模板（全租户共享，不门控 demo seed，生产也需预置）
		if err := pipeline.SeedTemplates(ctx, pipelineStore); err != nil {
			log.Printf("[seed] pipeline 模板失败: %v", err)
		}
		// workload seed 用 ApplyRepo（wlRepo 已装饰：写 PG + 投影 CRD），让 seed 工作负载真实落地
		// K8s（Deployment + nginx Pod）。seedPGAllIfEmpty 用 rawWl 不投影，故单独 seed；表空才灌（幂等）。
		if n, err := rawWl.WorkloadsCount(ctx); err != nil {
			log.Printf("[seed] 统计工作负载失败: %v", err)
		} else if n == 0 {
			seedWorkloads(ctx, wlRepo)
		}
		// 存量回填：为无 ServiceID 的既有工作负载幂等建 Service 实体（两路径同源，失败不阻断启动）。
		backfillTenantIDs(ctx, idb, svcRepo, wlRepo)
		log.Println("持久化后端: PostgreSQL（全 11 模块已迁移）")

		stores := &Stores{
			Identity:       idb,
			Application:    appRepo,
			Environment:    envRepo,
			AppConfig:      appcfgRepo,
			DataService:    dsRepo,
			Engine:         rawDs, // PG ds store 同实现 EngineRepository（平台级，无 ApplyRepo 装饰）
			Workload:       wlRepo,
			DevOpsRepos:    devopsRepo,
			DevOpsBuilds:   devopsRepo,
			DevOpsImages:   devopsRepo,
			DevOpsReleases: devopsRepo,
			Governance:     govRepoWithApply,
			ConfigCenter:   ccRepo,
			Billing:        billingRepo,
			Security:       secRepo,
			Messaging:      msgRepo,
			Backup:         bkRepo,
			MaaS:           maasRepo,
			KnowledgeBase:  kbRepo,
			Tool:           toolRepo,
			Prompt:         promptRepo,
			Agent:          agentRepo,
			Eval:           evalRepo,
			Pipeline:       pipelineStore,
			Change:         changeRepo,
			Service:        svcRepo,
			AlertRules:     obspg.NewStore(db),
		}
		return stores, db.Close, nil
	}

	// 内存路径：identity 走 seedIdentity，其余模块由 NewStore 内联 seed（保持现状）。
	idb := idmemory.NewStore()
	seedIdentity(idb, resolveAPIKey())
	appRepo := appmemory.NewStore()
	envRepo := envmemory.NewStore()
	appcfgRepo := appcfgmemory.NewStore()
	dsRaw := dsmemory.NewStore(dsmemory.WithNamespaceResolver(nsResolver))
	var dsRepo dataservice.Repository = dsRaw
	if appliers.dataservice != nil {
		dsRepo = dataservice.NewApplyRepo(dsRepo, appliers.dataservice) // K8s 启用：dataservice 写投影 CRD
	}
	// 内存路径 NewStore 传 imageRegistry opt，seed 镜像拼内网地址（集群可拉）。
	var wlRepo workload.Repository = wlmemory.NewStore(wlmemory.WithImageRegistry(os.Getenv("PAAS_IMAGE_REGISTRY"))) // 与 devops 共享：Release 编排更新 Workload.ImageRef
	if appliers.workload != nil {
		wlRepo = workload.NewApplyRepo(wlRepo, appliers.workload) // K8s 启用：workload 写操作投影 CRD 期望状态
	}
	svcRepo0 := svcmemory.NewStore()
	devopsRepo := devopsmemory.NewStore(wlRepo, devopsmemory.WithServiceLookup(serviceLookupBridge{repos: svcRepo0}))
	devopsRepo.SetPipeline(devopsPipeline) // PAAS_DEVOPS_REAL=true 接真实 git/docker
	govRepo := govmemory.NewStore()
	// K8s 启用：governance Route 写投影聚合 Ingress（内存路径同款，applier 持裸 govRepo）。
	var govRepoWithApply governance.Repository = govRepo
	if appliers.client != nil {
		routeApplier := controller.NewK8sRouteApplier(appliers.client, govRepo, govRepo, ingressClassFromEnv())
		govRepoWithApply = governance.NewApplyRepo(govRepo, routeApplier)
	}
	ccRepo := ccmemory.NewStore()
	billingRepo := billingmemory.NewStore()
	secRepo := secmemory.NewStore()
	maasRepo := maas.NewMemoryStore()
	seedMaasCatalog(ctx, maasRepo, secRepo)
	kbRepo := kbmemory.NewStore()
	toolRepo := toolmemory.NewStore()
	promptRepo := promptmemory.NewStore()
	agentRepo := agentmemory.NewStore()
	msgRepo := messaging.Repository(msgmemory.NewStore())
	bkRepo := backup.Repository(bkmemory.NewStore())
	pipelineStore := pipeline.NewMemoryStore()
	changeRepo := change.NewMemoryStore()
	svcRepo := svcRepo0
	// 存量回填：为无 ServiceID 的既有工作负载幂等建 Service 实体（两路径同源，失败不阻断启动）。
	backfillTenantIDs(ctx, idb, svcRepo, wlRepo)
	// 平台预置流水线模板（全租户共享，不门控 demo seed，生产也需预置）
	if err := pipeline.SeedTemplates(ctx, pipelineStore); err != nil {
		log.Printf("[seed] pipeline 模板失败: %v", err)
	}
	log.Println("持久化后端: 内存（dev/echo 路径，零依赖）")

	stores := &Stores{
		Identity:       idb,
		Application:    appRepo,
		Environment:    envRepo,
		AppConfig:      appcfgRepo,
		DataService:    dsRepo,
		Engine:         dsRaw, // 内存 ds store 同实现 EngineRepository（NewStore 已 seed DefaultEngines）
		Workload:       wlRepo,
		DevOpsRepos:    devopsRepo,
		DevOpsBuilds:   devopsRepo,
		DevOpsImages:   devopsRepo,
		DevOpsReleases: devopsRepo,
		Governance:     govRepoWithApply,
		ConfigCenter:   ccRepo,
		Billing:        billingRepo,
		Security:       secRepo,
		Messaging:      msgRepo,
		Backup:         bkRepo,
		MaaS:           maasRepo,
		KnowledgeBase:  kbRepo,
		Tool:           toolRepo,
		Prompt:         promptRepo,
		Agent:          agentRepo,
		Eval:           evalmemory.NewStore(),
		Pipeline:       pipelineStore,
		Change:         changeRepo,
		Service:        svcRepo,
	}
	return stores, nil, nil
}

// newDevOpsPipeline 按 PAAS_DEVOPS_BUILDER 选择构建流水线执行体：
//   - k8s：创建 K8s Job Pod（DooD，挂节点 docker.sock）跑 git clone→docker build→push，
//     core 轮询完成取日志解析 digest。需 K8s clientset 可用（集群内部署）。
//   - process：core 进程内 os/exec git/docker（本地 dev，distroless/K8s 部署不可用）。
//   - mock / 未设：nil（Store 默认 Mock，零依赖派生 digest，与历史一致）。
//
// 向后兼容：未设 PAAS_DEVOPS_BUILDER 但 PAAS_DEVOPS_REAL=true → process。
//
// 凭证 env（k8s/process 共用）：PAAS_REGISTRY、PAAS_GIT_TOKEN、
// PAAS_REGISTRY_USER / PAAS_REGISTRY_PASS。k8s 模式额外：PAAS_BUILDER_IMAGE（Job 容器镜像）。
func newDevOpsPipeline(appliers k8sAppliers) builder.Pipeline {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("PAAS_DEVOPS_BUILDER")))
	if mode == "" && envEnabled("PAAS_DEVOPS_REAL") { // 向后兼容
		mode = "process"
	}
	switch mode {
	case "k8s":
		if appliers.clientset == nil {
			log.Printf("DevOps: PAAS_DEVOPS_BUILDER=k8s 但 K8s clientset 不可用，降级为 Mock")
			return nil
		}
		log.Printf("DevOps: K8s Job 构建流水线已启用（构建 Job 落地按租户派生 ns paas-<tenant>, builderImage=%s）",
			builderImageEnv())
		return &builder.K8sJob{
			Clientset:    appliers.clientset,
			BuilderImage: builderImageEnv(),
			Registry:     os.Getenv("PAAS_REGISTRY"),
			GitToken:     os.Getenv("PAAS_GIT_TOKEN"),
			RegistryUser: os.Getenv("PAAS_REGISTRY_USER"),
			RegistryPass: os.Getenv("PAAS_REGISTRY_PASS"),
		}
	case "process":
		log.Printf("DevOps: in-process 构建流水线已启用（git clone + docker build + push，registry=%s）", os.Getenv("PAAS_REGISTRY")) //nolint:gosec // G706 误报：registry env 非用户输入
		return &builder.Real{
			Registry:     os.Getenv("PAAS_REGISTRY"),
			GitToken:     os.Getenv("PAAS_GIT_TOKEN"),
			RegistryUser: os.Getenv("PAAS_REGISTRY_USER"),
			RegistryPass: os.Getenv("PAAS_REGISTRY_PASS"),
		}
	default: // "" / "mock" / 未知值
		return nil
	}
}

// builderImageEnv 返回 Job 容器镜像（PAAS_BUILDER_IMAGE 或默认 docker:git）。
func builderImageEnv() string {
	if v := os.Getenv("PAAS_BUILDER_IMAGE"); v != "" {
		return v
	}
	return "docker:git"
}

// envEnabled 判定布尔 env（true/1/yes/on，大小写不敏感）。
func envEnabled(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// seedPGAllIfEmpty 仅在对应表为空时灌入预置数据（幂等，重启不重复灌、不刷日志）。
//
// 简单模块（identity/application/environment/appconfig/dataservice/workload/governance）
// 走 PG store 的 Count 判空 + Repository.Create 方法（无副作用）。
// 复杂模块（devops/configcenter/billing/security）调各自 SeedIfEmpty（直接 SQL INSERT，
// 绕过编排/状态机，保留 paid 历史账单、BuildSuccess 已完成状态、平台级 Secret NULL tenant_id 等特殊数据）。
//
// 入参为 PG store 具体类型（已在 buildAllStores 构造完毕），无需再做类型断言。
func seedPGAllIfEmpty(
	ctx context.Context,
	idb *identitypg.Store, appRepo *applicationpg.Store,
	envRepo *envpg.Store, appcfgRepo *appcfgpg.Store,
	dsRepo *dspg.Store, wlRepo *wlpg.Store,
	devopsRepo *devopspg.Store, govRepo *govpg.Store, ccRepo *ccpg.Store,
	billingRepo *billingpg.Store, secRepo *secpg.Store,
) {
	// identity：始终调 seedIdentity（逐实体幂等：租户/Key 走 ON CONFLICT，
	// 密码登录用户前置 GetUserByName 检查）。这样重部署到已有 PG 能自动补齐缺失的
	// 演示登录账号（acme-admin/acme-dev/globex-admin），与内存路径行为一致。
	// 原「TenantsCount==0 才灌」全有或全无策略，导致历史 DB（已有租户但缺演示用户）
	// 永不补齐 → console-user 演示账号快切登录失败。
	seedIdentity(idb, resolveAPIKey())
	// application：以 seed.TenantID 建 ctx，PG Create 以 ctx 为准。
	if n, err := appRepo.AppsCount(ctx); err != nil {
		log.Printf("[seed] 统计应用失败: %v", err)
	} else if n == 0 {
		seedApplications(ctx, appRepo)
	}
	if n, err := envRepo.EnvsCount(ctx); err != nil {
		log.Printf("[seed] 统计环境失败: %v", err)
	} else if n == 0 {
		seedEnvironments(ctx, envRepo)
	}
	if n, err := appcfgRepo.ConfigsCount(ctx); err != nil {
		log.Printf("[seed] 统计应用配置失败: %v", err)
	} else if n == 0 {
		seedAppConfigs(ctx, appcfgRepo)
	}
	if n, err := dsRepo.DataServicesCount(ctx); err != nil {
		log.Printf("[seed] 统计数据服务失败: %v", err)
	} else if n == 0 {
		seedDataServices(ctx, dsRepo)
	}
	// workload seed 移到 buildAllStores 用 ApplyRepo（投影 CRD，rawWl 不投影）。
	if n, err := govRepo.ServicesCount(ctx); err != nil {
		log.Printf("[seed] 统计治理服务失败: %v", err)
	} else if n == 0 {
		seedGovernance(ctx, govRepo)
	}
	// 复杂模块：直接调 PG store 内部 SeedIfEmpty（绕过 Create 编排/状态机）。
	if err := devopsRepo.SeedIfEmpty(ctx); err != nil {
		log.Printf("[seed] DevOps 失败: %v", err)
	}
	if err := ccRepo.SeedIfEmpty(ctx); err != nil {
		log.Printf("[seed] 配置中心失败: %v", err)
	}
	if err := billingRepo.SeedIfEmpty(ctx); err != nil {
		log.Printf("[seed] 计费失败: %v", err)
	}
	if err := secRepo.SeedIfEmpty(ctx); err != nil {
		log.Printf("[seed] 安全失败: %v", err)
	}
}

// seedApplications 把内存版同一批预置应用灌入目标仓储（PG 路径用）。
// 以每个应用自身的 TenantID 建 ctx（PG Create 以 ctx 租户为准），保证归属正确。
//
// PAAS_DISABLE_DEMO_SEED=true（chart seed.demo=false）时跳过，生产由用户自建。
func seedApplications(ctx context.Context, appRepo application.Repository) {
	if os.Getenv("PAAS_DISABLE_DEMO_SEED") == "true" {
		return
	}
	for _, a := range appmemory.SeedApps() {
		a.Recount()
		appCtx := tenant.WithTenant(ctx, a.TenantID)
		if err := appRepo.Create(appCtx, a); err != nil {
			log.Printf("[seed] 应用 %s: %v", a.ID, err)
		}
	}
}

// seedEnvironments 灌入环境预置数据（每条按 TenantID 建 ctx，PG Create 以 ctx 为准）。
func seedEnvironments(ctx context.Context, repo environment.Repository) {
	for _, e := range envmemory.SeedEnvs() {
		if err := repo.Create(tenant.WithTenant(ctx, e.TenantID), e); err != nil {
			log.Printf("[seed] 环境 %s: %v", e.ID, err)
		}
	}
}

// seedAppConfigs no-op（去假数据）：不灌 mock 应用配置（原 seed 含假 secret）。用户配置真实 env/Secret。
func seedAppConfigs(ctx context.Context, repo appconfig.Repository) {}

// seedDataServices no-op（去假数据）：不灌 mock 数据服务实例。
// 用户经控制台创建真实数据服务（已有真实引擎 mysql/redis/nats/minio）。
func seedDataServices(ctx context.Context, repo dataservice.Repository) {}

// seedEnginesIfEmpty 灌入默认引擎目录（平台级配置，非假数据）：表空才灌（幂等）。
// memory 路径 NewStore 已内联 seed，仅 PG 路径需调；managed 轻量引擎 enabled + 重型 external-shared 占位。
func seedEnginesIfEmpty(ctx context.Context, repo dataservice.EngineRepository) {
	n, err := repo.EnginesCount(ctx)
	if err != nil {
		log.Printf("[seed] 统计引擎失败: %v", err)
		return
	}
	if n > 0 {
		return
	}
	for _, e := range dataservice.DefaultEngines() {
		if _, err := repo.CreateEngine(ctx, e); err != nil {
			log.Printf("[seed] 灌引擎 %s 失败: %v", e.ID, err)
		}
	}
}

// seedWorkloads 灌入工作负载预置数据。
func seedWorkloads(ctx context.Context, repo workload.Repository) {
	// 内网部署 seed 镜像拼 PAAS_IMAGE_REGISTRY 前缀（集群节点可拉）；空用公开名（dev 内存模式）。
	registry := os.Getenv("PAAS_IMAGE_REGISTRY")
	for _, w := range wlmemory.SeedWorkloads(registry) {
		if err := repo.Create(tenant.WithTenant(ctx, w.TenantID), w); err != nil {
			log.Printf("[seed] 工作负载 %s: %v", w.ID, err)
		}
	}
}

// seedGovernance 灌入服务治理预置数据。灌入顺序：service → instance → route → breaker
// （instance.service_id 引用 service.id；PG 无外键约束，但按依赖顺序灌更直观）。
func seedGovernance(ctx context.Context, repo governance.Repository) {
	// 去假数据：不灌 mock 服务/实例/路由/熔断。用户配置产生。
	// 实例真源为 K8s Endpoints（/dp/ 数据面发现已提供），governance /api/instances 切 Endpoints 留后续。
}

// seedMaasCatalog 灌入模型目录（demo 模式 PAAS_DISABLE_DEMO_SEED != true）。
// PG store 支持 ModelsCount，表空才灌（幂等优化）；内存路径 SeedCatalog 自身幂等（exists 跳过）直接灌。
// resolver 用于 catalog() 构造真实通道（impl 不入库，MaaSPlugin.Init 加载时 BuildProvider 重建）。
func seedMaasCatalog(ctx context.Context, repo maas.Repository, secStore security.SecretStore) {
	if os.Getenv("PAAS_DISABLE_DEMO_SEED") == "true" {
		return
	}
	// 清理已废弃的旧 seed 模型（直连供应商占位 + mock/echo 演示），CASCADE 自动清通道。
	// 幂等：不存在的返 ErrModelNotFound 忽略；保证模型市场仅留 airouter 真实模型 + admin 手动配置。
	for _, id := range maas.DeprecatedSeedModelIDs {
		if err := repo.DeleteModel(ctx, id); err != nil && !errors.Is(err, maas.ErrModelNotFound) {
			log.Printf("[seed] 清理废弃模型 %s 失败: %v", id, err)
		}
	}
	// ensure airouter 预置供应商（不被 ModelsCount 跳过；admin 可见可改，创建通道选它即带入）。
	if err := repo.CreateVendor(ctx, maas.AirouterVendor()); err != nil && !errors.Is(err, maas.ErrVendorExists) {
		log.Printf("[seed] airouter vendor 失败: %v", err)
	}
	// PG store 暴露 ModelsCount，表非空跳过（避免重启重复尝试 insert 全部模型）。
	if counter, ok := repo.(interface {
		ModelsCount(context.Context) (int, error)
	}); ok {
		n, err := counter.ModelsCount(ctx)
		if err != nil {
			log.Printf("[seed] 统计模型失败: %v", err)
			return
		}
		if n > 0 {
			return
		}
	}
	if err := maas.SeedCatalog(ctx, repo, secretResolver{store: secStore}); err != nil {
		log.Printf("[seed] MaaS catalog: %v", err)
	}
}
