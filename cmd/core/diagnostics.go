package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitoys/paas/internal/diagnostics"
	"github.com/aitoys/paas/internal/httputil"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// diagProvider 生成平台诊断快照（装配层：读环境/K8s/DB；internal/diagnostics 只定义模型与 handler）。
//
// 只读约束：全部检查都是读操作（DB ping/SELECT、K8s discovery/SSAR、HTTP GET 探测），
// 不写集群、不改数据、不触发 reconcile。安全配置经 SecurityChecks 只报状态，不回显值。
// processStartedAt 进程启动时刻（uptime 基线；包级变量在 main 加载时确定）。
var processStartedAt = time.Now()

type diagProvider struct {
	startedAt time.Time
	clientset kubernetes.Interface
	db        *storagepg.DB
}

// snapshot 汇总一次诊断快照。
func (p diagProvider) snapshot(ctx context.Context) diagnostics.Snapshot {
	now := time.Now()
	deploy := p.deployInfo(ctx)
	storage := p.storageInfo(ctx)
	return diagnostics.Snapshot{
		Version:      buildVersionInfo(),
		Runtime:      runtimeInfo(p.startedAt, now),
		Deploy:       deploy,
		Storage:      storage,
		Dependencies: p.dependencies(ctx, deploy, storage),
		Security:     diagnostics.SecurityChecks(os.Getenv),
		Notes: []string{
			"漂移检查不在此处：工作负载「对账修复」与 scripts/reconcile.sh 承担，本页只读不触发写入。",
			"事件总线为 Core 内置（NATS 嵌入式），不作为外部依赖探测。",
			"依赖探测只报可达性（2s 超时），不回显上游地址；failed 不代表数据面不可用，可能仅本 Pod 网络受限。",
		},
		GeneratedAt: now.Format(time.RFC3339),
	}
}

// buildVersionInfo 从 Go 构建信息读取版本/修订（无 ldflags 时如实显示 devel）。
func buildVersionInfo() diagnostics.VersionInfo {
	v := diagnostics.VersionInfo{Version: "(devel)", GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	v.Module = bi.Main.Path
	v.Version = bi.Main.Version
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.modified":
			v.Modified = s.Value
		}
	}
	return v
}

func runtimeInfo(startedAt, now time.Time) diagnostics.RuntimeInfo {
	return diagnostics.RuntimeInfo{
		StartedAt:     startedAt.Format(time.RFC3339),
		UptimeSeconds: int64(now.Sub(startedAt).Seconds()),
		Goroutines:    runtime.NumGoroutine(),
	}
}

// deployInfo 部署模式 + 集群连通性 + RBAC 只读探测。
func (p diagProvider) deployInfo(ctx context.Context) diagnostics.DeployInfo {
	info := diagnostics.DeployInfo{
		Mode:               "local",
		KubeconfigSource:   kubeconfigSource(),
		NamespaceScope:     os.Getenv("PAAS_K8S_NAMESPACE"),
		Connectivity:       "not_configured",
		RBACListWorkloads:  "unknown",
		RBACListNamespaces: "unknown",
	}
	if p.clientset == nil {
		return info
	}
	info.Mode = "kubernetes"
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ver, err := p.clientset.Discovery().ServerVersion()
	if err != nil {
		info.Connectivity = "failed"
		info.Error = "集群 API 不可达或版本查询被拒" // 不回显上游错误细节（可能含地址）
		log.Printf("诊断: K8s 版本查询失败: %v", err)
	} else {
		info.Connectivity = "ok"
		info.ServerVersion = ver.String()
	}
	info.RBACListWorkloads = p.rbacCheck(cctx, "list", "core.aitoys.github.com", "workloads", info.NamespaceScope)
	info.RBACListNamespaces = p.rbacCheck(cctx, "list", "", "namespaces", "")
	return info
}

// rbacCheck 用 SelfSubjectAccessReview 验证当前凭证是否具备某只读权限（不实际执行该操作）。
func (p diagProvider) rbacCheck(ctx context.Context, verb, group, resource, ns string) string {
	res, err := p.clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
		&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Namespace: ns},
		}}, metav1.CreateOptions{})
	if err != nil {
		return "unknown"
	}
	if res.Status.Allowed {
		return "allowed"
	}
	return "denied"
}

func kubeconfigSource() string {
	switch {
	case os.Getenv("KUBERNETES_SERVICE_HOST") != "":
		return "in-cluster"
	case os.Getenv("PAAS_KUBECONFIG") != "":
		return "PAAS_KUBECONFIG"
	case os.Getenv("KUBECONFIG") != "":
		return "KUBECONFIG"
	default:
		return "none"
	}
}

// storageInfo 元数据存储后端 + ping 延迟 + 迁移版本。
// 迁移版本读 golang-migrate 的 schema_migrations（读不到就 unknown，不猜）。
func (p diagProvider) storageInfo(ctx context.Context) diagnostics.StorageInfo {
	if p.db == nil {
		return diagnostics.StorageInfo{Backend: "memory", MigrationVersion: "n/a"}
	}
	info := diagnostics.StorageInfo{Backend: "postgres", MigrationVersion: "unknown"}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := p.db.Pool().Ping(cctx); err != nil {
		info.Error = "元数据库 ping 失败"
		log.Printf("诊断: 元数据库 ping 失败: %v", err)
		return info
	}
	info.PingOK = true
	info.LatencyMS = time.Since(start).Milliseconds()
	var version int64
	var dirty bool
	if err := p.db.Pool().QueryRow(cctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty); err != nil {
		info.Error = "迁移版本读取失败（schema_migrations 不可读）"
		return info
	}
	info.MigrationVersion = strconv.FormatInt(version, 10)
	info.MigrationDirty = dirty
	return info
}

// dependencies 外部依赖配置与可达性（观测三支柱）。复用已查到的 storage/deploy 结论，不重复探测。
func (p diagProvider) dependencies(ctx context.Context, deploy diagnostics.DeployInfo, storage diagnostics.StorageInfo) []diagnostics.Dependency {
	deps := []diagnostics.Dependency{
		{Name: "元数据库", Kind: "database", Configured: p.db != nil, Reachable: depReach(p.db != nil, storage.PingOK)},
		{Name: "Kubernetes API", Kind: "kubernetes", Configured: p.clientset != nil, Reachable: depReach(p.clientset != nil, deploy.Connectivity == "ok")},
	}
	probes := []struct {
		name, env, path, kind, note string
	}{
		{"Prometheus", "PAAS_PROM_URL", "/-/ready", "metrics", "应用/集群指标与采集质量诊断的数据源"},
		{"Loki", "PAAS_LOKI_URL", "/ready", "logs", "日志查询与 trace-log 关联的数据源"},
		{"Jaeger", "PAAS_JAEGER_URL", "/api/services", "tracing", "链路查询数据源"},
	}
	for _, pr := range probes {
		base := os.Getenv(pr.env)
		d := diagnostics.Dependency{Name: pr.name, Kind: pr.kind, Configured: base != "", Reachable: "not_configured", Note: pr.note}
		if base != "" {
			d.Reachable = probeHTTP(ctx, base, pr.path)
		}
		deps = append(deps, d)
	}
	return deps
}

func depReach(configured, ok bool) string {
	switch {
	case !configured:
		return "not_configured"
	case ok:
		return "ok"
	default:
		return "failed"
	}
}

// probeHTTP 2s 内 GET base+path；200-399 视为可达（不跟随重定向）。
// 上游错误不外泄（调用方只拿状态）。
func probeHTTP(ctx context.Context, base, path string) string {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	//nolint:gosec // base/path 来自部署配置（PAAS_*_URL）与固定探活路径，非用户可控输入
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+path, nil)
	if err != nil {
		return "failed"
	}
	resp, err := httputil.NewClient(2 * time.Second).Do(req) //nolint:gosec // 同上：目标由部署配置决定
	if err != nil {
		return "failed"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		return "ok"
	}
	return "failed"
}
