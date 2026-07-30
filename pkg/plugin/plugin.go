// Package plugin 定义平台子系统接入 Platform Core 的契约。
// 子系统（如 MaaS）通过实现 Plugin 接口注册到 Core，由 Core 注入依赖。
package plugin

import (
	"context"

	"github.com/aitoys/paas/pkg/provider"
)

// Manifest 声明插件元信息。
type Manifest struct {
	Name    string   // 插件名，如 "maas"；全平台唯一
	Version string   // 语义化版本
	Depends []string // 依赖的其他插件名（Core 解析加载顺序）
}

// RouteSpec 声明插件暴露给 API Gateway 的路由。
type RouteSpec struct {
	Path    string // 含方法的路径，如 "POST /v1/chat/completions"
	Require string // 所需权限标识，如 "maas:infer"
}

// CRDSchema 声明由 Core 统一注册到 K8s 的 CRD（本期仅承载定义）。
type CRDSchema struct {
	Group   string
	Version string
	Kind    string
	Plural  string
}

// MeterSpec 声明插件产出的计量事件类型。
type MeterSpec struct {
	Name string // 如 "tokens"
	Unit string // 如 "count"
}

// CoreDeps 由 Core 在 Init 阶段注入；插件不得自行构造外部连接。
// 具体字段在 Plan 2 逐步补全（DB / EventBus / Provider / OTel），
// 本期以接口形式预留，避免循环依赖。
type CoreDeps interface {
	// Logger 返回带租户/插件上下文的日志器（Plan 2 实现）。
	Logger() interface{}
	// Gateway 返回推理路由注册点；插件在 Init 阶段把 Provider 注册进去。
	// 非 MaaS 类插件可返回 nil。
	Gateway() provider.GatewayRegistrar
	// SecretResolver 返回平台级 Secret 明文解析器；非 MaaS 类插件可返回 nil。
	// 用于第三方供应商通道经 CredentialRef 解析出 API Key（仅内存，不日志/不持久化）。
	SecretResolver() provider.CredentialResolver
}

// Plugin 是子系统接入 Core 必须实现的契约。
type Plugin interface {
	Manifest() Manifest
	Routes() []RouteSpec
	Schemas() []CRDSchema
	Meters() []MeterSpec
	Init(ctx context.Context, deps CoreDeps) error
	Run(ctx context.Context) error
}
