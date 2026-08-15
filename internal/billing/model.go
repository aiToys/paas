// Package billing 是配额计费领域模型（多租户商业化根基）：租户级资源配额 + 用量 + 账单。
//
// 定位属「设置」维度（租户级管理面），租户私有；独立于物理环境，不接 prod:write。
// 复用 API Key 三元组作为计量锚点，租户上下文走 pkg/tenant ctx。
//
// 本期只做配额/用量/账单的查询展示 + 账单生成/支付闭环（进程内 mock）；
// 配额强制拦截（资源创建阻断超限）、真实计量采集、计费引擎/支付网关留后续。
package billing

import (
	"errors"
	"time"
)

// ErrQuotaExceeded 资源用量超配额上限（横切配额拦截）。各资源 Create 前由 CheckAndInc 检查，
// 超限时拒绝创建并回 429。
var ErrQuotaExceeded = errors.New("配额超限")

// 计费资源维度（账单与配额的 key）。
const (
	ResApplications = "applications" // 应用数
	ResWorkloads    = "workloads"    // 工作负载数
	ResModels       = "models"       // 模型部署数
	ResDataservices = "dataservices" // 数据服务实例数
	ResGPU          = "gpu"          // GPU 卡·小时
	ResTokens       = "tokens"       // token（千次）
	ResStorage      = "storage_gb"   // 存储 GB
)

// PriceTable 是各资源的平台级 mock 单价（元/单位）。
// 真实计费引擎/阶梯套餐留后续；导出供前端对齐展示。
var PriceTable = map[string]float64{
	ResApplications: 10.0,
	ResWorkloads:    5.0,
	ResModels:       20.0,
	ResDataservices: 8.0,
	ResGPU:          100.0,
	ResTokens:       0.001,
	ResStorage:      0.5,
}

// ResourceOrder 定义资源的稳定展示顺序（前端卡片/账单明细按此排列）。
var ResourceOrder = []string{
	ResApplications, ResWorkloads, ResModels, ResDataservices, ResGPU, ResTokens, ResStorage,
}

// Unlimited 表示无配额上限（Limits[res] = -1）。
const Unlimited = -1

// 账单状态。
const (
	StatusUnpaid = "unpaid"
	StatusPaid   = "paid"
)

// ResourceQuota 租户级资源配额（每租户一份）。Limits[res] = 上限，-1 = 无限。
type ResourceQuota struct {
	TenantID  string         `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Limits    map[string]int `json:"limits"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// ResourceUsage 租户级当前用量计数。
// ByApp 是应用维度归因（appID → resource → 用量），主要给模型推理 token 计费：
// 应用级 API Key 调用 /v1 时，gateway 把 appID 注入 ctx，IncUsage 据此归位。
// 账单可按应用拆"订单应用用了多少 token / 推荐应用用了多少"。
type ResourceUsage struct {
	TenantID  string                    `json:"tenantId,omitempty"`
	Counts    map[string]int            `json:"counts"`
	ByApp     map[string]map[string]int `json:"byApp,omitempty"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}

// BillItem 账单明细项。
type BillItem struct {
	Resource  string  `json:"resource"`
	Quantity  int     `json:"quantity"`
	UnitPrice float64 `json:"unitPrice"`
	Amount    float64 `json:"amount"`
}

// BillingRecord 计费账单（按周期，状态机 unpaid -> paid）。
type BillingRecord struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenantId,omitempty"`
	Period    string     `json:"period"` // YYYY-MM
	Items     []BillItem `json:"items"`
	Total     float64    `json:"total"`
	Status    string     `json:"status"` // unpaid | paid
	CreatedAt time.Time  `json:"createdAt"`
	PaidAt    *time.Time `json:"paidAt,omitempty"`
}

// UsageLine 是单资源的用量视图行（用量 + 上限 + 超限标记）。
type UsageLine struct {
	Resource string `json:"resource"`
	Count    int    `json:"count"`
	Limit    int    `json:"limit"` // -1 = 无限
	Over     bool   `json:"over"`  // Count > Limit（Limit != -1 时）
}

// UsageView 是用量查询的组装返回（配额 + 用量 + 逐项行）。
type UsageView struct {
	Quota ResourceQuota `json:"quota"`
	Usage ResourceUsage `json:"usage"`
	Items []UsageLine   `json:"items"`
}

// BuildUsageView 按 ResourceOrder 组装用量视图，标注超限。
func BuildUsageView(q ResourceQuota, u ResourceUsage) UsageView {
	lines := make([]UsageLine, 0, len(ResourceOrder))
	for _, res := range ResourceOrder {
		limit := Unlimited
		if l, ok := q.Limits[res]; ok {
			limit = l
		}
		count := u.Counts[res]
		over := limit != Unlimited && count > limit
		lines = append(lines, UsageLine{Resource: res, Count: count, Limit: limit, Over: over})
	}
	return UsageView{Quota: q, Usage: u, Items: lines}
}

// ValidatePeriod 校验账单周期格式 YYYY-MM（含真实月份范围，拒绝 2026-13 等）。
func ValidatePeriod(p string) error {
	if _, err := time.Parse("2006-01", p); err != nil {
		return errInvalid("period")
	}
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
