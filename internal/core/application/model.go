// Package application 是 Platform Core 的「应用」领域模型。
// 应用是统一控制台的主线抽象：各类资源（模型推理/MQ/DAL/治理）以绑定形式归属应用，
// 随应用生命周期联动。对齐前端 console-user 的 Application 契约。
package application

import (
	"context"
	"fmt"
)

// ResourceCount 记录应用绑定的各类资源数量。
// 由 Bindings 派生（recount），不作为独立真源，避免计数与绑定项不一致。
type ResourceCount struct {
	Models int `json:"models"`
	MQ     int `json:"mq"`
	DAL    int `json:"dal"`
}

// Binding 是应用与单个资源的绑定关系。
// Type 取值：models / mq / dal / gov（服务治理）。
type Binding struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"` // 备注，如规格、副本等
}

// AppStats 是应用下工作负载的聚合统计（派生 Replicas/Status 用，非真源）。
type AppStats struct {
	Ready     int // 就绪副本合计
	Total     int // 期望副本合计
	Deploying int // 部署中的工作负载数
	Failed    int // 异常的工作负载数
}

// WorkloadStats 提供租户内各应用的工作负载聚合统计，供应用列表派生 Replicas/Status
// （依赖倒置：application 不依赖 workload）。未注入时 handler 透传 seed 原值（降级）。
type WorkloadStats interface {
	StatsByTenant(ctx context.Context) (map[string]AppStats, error)
}

// Application 是平台应用实体。
type Application struct {
	ID        string        `json:"id"`
	TenantID  string        `json:"tenantId,omitempty"` // 多租户隔离键；由 Repository 从 ctx 写入，请求体忽略
	Name      string        `json:"name"`
	Initial   string        `json:"initial"`
	Env       string        `json:"env"`    // 生产 / 预发 / 开发
	Status    string        `json:"status"` // healthy / degraded / idle
	Gradient  string        `json:"gradient"`
	Desc      string        `json:"desc"`
	Resources ResourceCount `json:"resources"` // 派生：各类资源计数
	Bindings  []Binding     `json:"bindings"`  // 真源：具体绑定项
	Replicas   string        `json:"replicas"`
	RPS        string        `json:"rps"`
	// Restricted 开启应用级权限 enforcement（成员角色制）。
	// false（默认）= 租户级 RBAC 即可写（现状，向后兼容）；true = 写操作需应用成员角色匹配
	// （如 app-developer 不可发布）。渐进启用：存量应用默认关闭，管理员按需开启。
	Restricted bool `json:"restricted"`
}

// Recount 根据 Bindings 重算 ResourceCount。
// 仅统计 models/mq/dal 三类（列表页展示维度）；gov 等其它类型不计入计数但仍保留在 Bindings 中。
func (a *Application) Recount() {
	a.Resources = ResourceCount{}
	for _, b := range a.Bindings {
		switch b.Type {
		case "models":
			a.Resources.Models++
		case "mq":
			a.Resources.MQ++
		case "dal":
			a.Resources.DAL++
		}
	}
}

// ApplyStats 用工作负载聚合统计派生 Replicas/Status（覆盖 seed 静态假值，真实化）。
// 规则：无工作负载 -> idle/"0/0"；有 failed 或未全部就绪 -> degraded；全就绪 -> healthy。
// RPS 需应用级 metrics 埋点（留后续），暂清空不展示假值。
func (a *Application) ApplyStats(st AppStats) {
	a.Replicas = fmt.Sprintf("%d/%d", st.Ready, st.Total)
	switch {
	case st.Total == 0:
		a.Status = "idle"
	case st.Failed > 0 || st.Ready < st.Total:
		a.Status = "degraded"
	default:
		a.Status = "healthy"
	}
	a.RPS = ""
}

// ApplyDefaults 补齐 Create 时缺失的展示字段（API 创建兜底，避免前端卡片图标/徽标空白）。
// ID/TenantID 由 handler/repository 单独处理（ID 时间戳生成，TenantID 从 ctx 写入）。
func (a *Application) ApplyDefaults() {
	if a.Initial == "" && a.Name != "" {
		for _, r := range a.Name { // 取 Name 首个 rune 作图标字母（兼容中文）
			a.Initial = string(r)
			break
		}
	}
	if a.Status == "" {
		a.Status = "idle"
	}
	if a.Env == "" {
		a.Env = "开发"
	}
	if a.Gradient == "" {
		a.Gradient = "linear-gradient(135deg,#64748b,#475569)"
	}
}
