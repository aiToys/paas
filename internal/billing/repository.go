package billing

import "context"

// Repository 是配额计费持久化接口（配额 + 用量 + 账单）。
// 方法名带前缀以避免单 Store 实现时的重名冲突。
// 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	QuotaStore
	UsageStore
	BillStore
}

// QuotaStore 配额仓储（每租户一份）。
type QuotaStore interface {
	// GetQuota 读取配额；不存在返回默认配额（非错误）。
	GetQuota(ctx context.Context) (ResourceQuota, error)
	// SetQuota 覆盖更新配额（admin）。
	SetQuota(ctx context.Context, q ResourceQuota) (ResourceQuota, error)
	// ListAllQuotas 跨租户列出全部配额（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllQuotas(ctx context.Context) ([]ResourceQuota, error)
}

// UsageStore 用量仓储。
type UsageStore interface {
	// GetUsage 读取当前用量。
	GetUsage(ctx context.Context) (ResourceUsage, error)
	// IncUsage 增减某资源用量（delta 可负）；演示用，真实计量采集留后续。
	IncUsage(ctx context.Context, resource string, delta int) (ResourceUsage, error)
	// CheckAndInc 原子「检查超限 + 递增」：若 usage[resource]+delta 超配额上限（limit>0）
	// 返 ErrQuotaExceeded（不递增）；limit=-1（无限）或未设配额时不拦截。
	// 横切：各资源 Create 前调用，超限阻断创建。
	CheckAndInc(ctx context.Context, resource string, delta int) (ResourceUsage, error)
}

// BillStore 账单仓储。
type BillStore interface {
	// ListBills 账单列表（按 CreatedAt 倒序）。
	ListBills(ctx context.Context) ([]BillingRecord, error)
	// GenerateBill 按当前用量 × 单价生成 period 账单；同 period 已有 unpaid 则覆盖。
	GenerateBill(ctx context.Context, period string) (BillingRecord, error)
	// GetBill 读取单条账单（跨租户 not found）。
	GetBill(ctx context.Context, id string) (BillingRecord, error)
	// PayBill 支付账单（unpaid -> paid）；已支付或不存在报错。
	PayBill(ctx context.Context, id string) (BillingRecord, error)
	// ListAllBills 跨租户列出全部账单（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllBills(ctx context.Context) ([]BillingRecord, error)
}
