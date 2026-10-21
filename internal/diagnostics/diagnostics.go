// Package diagnostics 提供平台只读诊断快照（版本 / 部署模式 / 存储与迁移 / 依赖 / K8s 与 RBAC / 安全配置状态）。
//
// 定位：admin「平台诊断」页的只读检查入口。约束（spec D4）：
//   - 全部检查只读：不改集群、不改数据、不触发 reconcile；
//   - 安全配置只报「已配置 / 未配置」状态，**绝不回显配置值**（密钥、DSN、令牌一律不出现在响应里）；
//   - 无法确认的项如实标 unknown，不用零值/默认值冒充「正常」；
//   - 漂移检测不在本包：走 reconcile dry-run（独立只读动作），此处只列检查入口提示。
package diagnostics

import (
	"context"
	"net/http"

	"github.com/aitoys/paas/internal/httputil"
)

// VersionInfo 构建版本（来自 Go 构建信息，无 ldflags 时如实显示 devel）。
type VersionInfo struct {
	Module    string `json:"module"`
	Version   string `json:"version"`
	Revision  string `json:"revision,omitempty"`
	Modified  string `json:"modified,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// RuntimeInfo 进程运行态。
type RuntimeInfo struct {
	StartedAt     string `json:"startedAt"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	Goroutines    int    `json:"goroutines"`
}

// DeployInfo 部署模式与集群连通性（不含集群凭证）。
type DeployInfo struct {
	Mode             string `json:"mode"`             // kubernetes | local
	KubeconfigSource string `json:"kubeconfigSource"` // in-cluster | PAAS_KUBECONFIG | none
	NamespaceScope   string `json:"namespaceScope"`   // 空 = 全命名空间
	ServerVersion    string `json:"serverVersion,omitempty"`
	Connectivity     string `json:"connectivity"` // ok | failed | not_configured
	Error            string `json:"error,omitempty"`
	// RBAC 探测（SelfSubjectAccessReview，只读）：
	RBACListWorkloads  string `json:"rbacListWorkloads"`  // allowed | denied | unknown
	RBACListNamespaces string `json:"rbacListNamespaces"` // allowed | denied | unknown
}

// StorageInfo 元数据存储与迁移状态。
type StorageInfo struct {
	Backend          string `json:"backend"` // postgres | memory
	PingOK           bool   `json:"pingOk"`
	LatencyMS        int64  `json:"latencyMs"`
	MigrationVersion string `json:"migrationVersion"` // 迁移版本号；unknown = 无法确认
	MigrationDirty   bool   `json:"migrationDirty"`
	Error            string `json:"error,omitempty"`
}

// Dependency 外部依赖的配置与可达性（只报状态，不回显 URL）。
type Dependency struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // database | kubernetes | metrics | logs | tracing | bus
	Configured bool   `json:"configured"`
	Reachable  string `json:"reachable"` // ok | failed | not_configured
	Note       string `json:"note,omitempty"`
}

// ConfigCheck 安全配置状态（**只有状态，没有值**）。
type ConfigCheck struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"`   // set | unset | enabled | disabled | on | off
	Severity string `json:"severity"` // info | warn
	Note     string `json:"note,omitempty"`
}

// Snapshot 诊断快照（单次请求实时生成）。
type Snapshot struct {
	Version      VersionInfo   `json:"version"`
	Runtime      RuntimeInfo   `json:"runtime"`
	Deploy       DeployInfo    `json:"deploy"`
	Storage      StorageInfo   `json:"storage"`
	Dependencies []Dependency  `json:"dependencies"`
	Security     []ConfigCheck `json:"security"`
	Notes        []string      `json:"notes"`
	GeneratedAt  string        `json:"generatedAt"`
}

// Provider 生成诊断快照（cmd/core 装配层实现，负责读取环境/K8s/DB）。
type Provider func(ctx context.Context) Snapshot

// Handler 暴露只读诊断端点（GET）。
func Handler(p Provider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		httputil.WriteData(w, p(r.Context()))
	})
}

// SecurityChecks 生成安全配置状态检查（纯函数，便于锁「不泄漏值」）。
// getenv 由装配层传入 os.Getenv；判空 == 未配置，**不读取具体值参与输出**。
func SecurityChecks(getenv func(string) string) []ConfigCheck {
	prod := getenv("PAAS_PROD") == "true"
	jwt := getenv("PAAS_JWT_SECRET")
	master := getenv("PAAS_SECRET_MASTER_KEY")
	cookieSecure := getenv("PAAS_COOKIE_SECURE") == "true"

	checks := []ConfigCheck{
		{
			Key: "prod_mode", Label: "生产模式（PAAS_PROD）",
			Status: boolStatus(prod, "on", "off"), Severity: "info",
			Note: "生产模式下强制 JWT secret ≥32 字节且 Cookie Secure，否则拒绝启动",
		},
		{
			Key: "jwt_secret", Label: "JWT 签名密钥（PAAS_JWT_SECRET）",
			Status: boolStatus(jwt != "", "set", "unset"), Severity: severity(!prod || len(jwt) >= 32),
			Note: jwtNote(prod, len(jwt)),
		},
		{
			Key: "cookie_secure", Label: "会话 Cookie Secure（PAAS_COOKIE_SECURE）",
			Status: boolStatus(cookieSecure, "enabled", "disabled"), Severity: severity(!prod || cookieSecure),
			Note: cookieNote(prod, cookieSecure),
		},
		{
			Key: "secret_master_key", Label: "敏感数据主密钥（PAAS_SECRET_MASTER_KEY）",
			Status: boolStatus(master != "", "set", "unset"), Severity: severity(!prod || master != ""),
			Note: masterNote(prod, master != ""),
		},
	}
	return checks
}

func boolStatus(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// severity：生产模式下缺失即 warn；非生产缺失只是 info（dev 允许内存/无密钥）。
func severity(ok bool) string {
	if ok {
		return "info"
	}
	return "warn"
}

func jwtNote(prod bool, n int) string {
	switch {
	case n == 0 && prod:
		return "生产模式未配置，服务会拒绝启动（当前能运行说明与配置不一致，请核对部署环境）"
	case n == 0:
		return "未配置：会话签名使用开发默认值，仅限本地"
	case n < 32:
		return "长度不足 32 字节，生产模式会拒绝启动"
	default:
		return "已配置（长度满足要求）"
	}
}

func cookieNote(prod, secure bool) string {
	switch {
	case secure:
		return "已启用"
	case prod:
		return "生产模式未启用，服务会拒绝启动"
	default:
		return "未启用：仅 HTTP 场景可接受"
	}
}

func masterNote(prod, set bool) string {
	switch {
	case set:
		return "已配置：secret/凭证按 enc:v1 静态加密"
	case prod:
		return "生产模式未配置，服务会拒绝启动（敏感数据将无法加密）"
	default:
		return "未配置：开发模式下凭证以明文存储"
	}
}
