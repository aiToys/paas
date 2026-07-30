// Package memory 提供 billing.Repository 的内存实现，seed 跨两租户示例配额/用量/账单。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 billing.Repository（QuotaStore + UsageStore + BillStore），单 Store 避免重名。
type Store struct {
	mu     sync.RWMutex
	quotas map[string]billing.ResourceQuota // tenantID -> quota
	usage  map[string]billing.ResourceUsage // tenantID -> usage
	bills  []billing.BillingRecord          // 全租户；查询时按 tenant 过滤
	seq    int
}

func NewStore() *Store {
	s := &Store{
		quotas: map[string]billing.ResourceQuota{},
		usage:  map[string]billing.ResourceUsage{},
	}
	s.seed()
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

// DefaultQuota 返回新租户的默认配额（各资源适度上限）。
func DefaultQuota(tid string, at time.Time) billing.ResourceQuota {
	return billing.ResourceQuota{
		TenantID: tid,
		Limits: map[string]int{
			billing.ResApplications: 50,
			billing.ResWorkloads:    100,
			billing.ResModels:       10,
			billing.ResGPU:          8,
			billing.ResTokens:       billing.Unlimited, // token 默认不限
			billing.ResStorage:      500,
		},
		UpdatedAt: at,
	}
}

// cloneIntMap 深拷贝 map，确保返回值/写入值与对端独立，避免并发 map 读写 panic。
func cloneIntMap(m map[string]int) map[string]int {
	cp := make(map[string]int, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// —— Quota ——

func (s *Store) GetQuota(ctx context.Context) (billing.ResourceQuota, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.quotas[tid]
	if !ok {
		return DefaultQuota(tid, time.Now()), nil
	}
	q.Limits = cloneIntMap(q.Limits) // 返回前深拷贝，与 store 独立
	return q, nil
}

func (s *Store) SetQuota(ctx context.Context, q billing.ResourceQuota) (billing.ResourceQuota, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q.TenantID = tid
	q.UpdatedAt = time.Now()
	q.Limits = cloneIntMap(q.Limits) // 写入前深拷贝，隔离 caller 传入的 map
	s.quotas[tid] = q
	return q, nil
}

// —— Usage ——

func (s *Store) GetUsage(ctx context.Context) (billing.ResourceUsage, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.usage[tid]
	if !ok {
		return billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}, UpdatedAt: time.Now()}, nil
	}
	u.Counts = cloneIntMap(u.Counts) // 返回前深拷贝
	return u, nil
}

func (s *Store) IncUsage(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[tid]
	if !ok {
		u = billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}}
	}
	u.Counts[resource] += delta
	u.UpdatedAt = time.Now()
	s.usage[tid] = u
	u.Counts = cloneIntMap(u.Counts) // 返回前深拷贝
	return u, nil
}

// CheckAndInc 原子「检查超限 + 递增」：limit>0 且 usage+delta 超限返 ErrQuotaExceeded（不递增）；
// limit<=0（Unlimited 或未设）不拦截。与 IncUsage 同锁，保证检查-递增原子（横切配额拦截基石）。
func (s *Store) CheckAndInc(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.quotas[tid]
	if !ok {
		q = DefaultQuota(tid, time.Now())
	}
	limit := q.Limits[resource]
	if limit == 0 {
		limit = billing.Unlimited // 未设配额项视为无限
	}
	u, ok := s.usage[tid]
	if !ok {
		u = billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}}
	}
	if limit > 0 && u.Counts[resource]+delta > limit {
		return billing.ResourceUsage{}, fmt.Errorf("%w: %s（上限 %d，当前 %d，请求 %+d）",
			billing.ErrQuotaExceeded, resource, limit, u.Counts[resource], delta)
	}
	u.Counts[resource] += delta
	u.UpdatedAt = time.Now()
	s.usage[tid] = u
	u.Counts = cloneIntMap(u.Counts)
	return u, nil
}

// —— Bills ——

func (s *Store) ListBills(ctx context.Context) ([]billing.BillingRecord, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.BillingRecord, 0)
	for _, b := range s.bills {
		if b.TenantID == tid {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.bills {
		if b.ID == id && b.TenantID == tid {
			return b, nil
		}
	}
	return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
}

// GenerateBill 按当前用量 × 单价生成 period 账单；同 period 已有 unpaid 则覆盖更新。
func (s *Store) GenerateBill(ctx context.Context, period string) (billing.BillingRecord, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	if err := billing.ValidatePeriod(period); err != nil {
		return billing.BillingRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.usage[tid]
	if !ok {
		u = billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}}
	}

	items := make([]billing.BillItem, 0, len(billing.ResourceOrder))
	total := 0.0
	for _, res := range billing.ResourceOrder {
		qty := u.Counts[res]
		if qty <= 0 {
			continue
		}
		price := billing.PriceTable[res]
		amt := float64(qty) * price
		items = append(items, billing.BillItem{Resource: res, Quantity: qty, UnitPrice: price, Amount: amt})
		total += amt
	}

	now := time.Now()
	// 同 period 已有 unpaid 则覆盖（避免重复账单堆积）
	for i, b := range s.bills {
		if b.TenantID == tid && b.Period == period && b.Status == billing.StatusUnpaid {
			b.Items = items
			b.Total = total
			b.CreatedAt = now
			s.bills[i] = b
			return b, nil
		}
	}

	s.seq++
	rec := billing.BillingRecord{
		ID:        fmt.Sprintf("bill-%s-%d", tid, s.seq),
		TenantID:  tid,
		Period:    period,
		Items:     items,
		Total:     total,
		Status:    billing.StatusUnpaid,
		CreatedAt: now,
	}
	s.bills = append(s.bills, rec)
	return rec, nil
}

func (s *Store) PayBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.bills {
		if b.ID != id || b.TenantID != tid {
			continue
		}
		if b.Status == billing.StatusPaid {
			return billing.BillingRecord{}, fmt.Errorf("账单已支付: %s", id)
		}
		now := time.Now()
		b.Status = billing.StatusPaid
		b.PaidAt = &now
		s.bills[i] = b
		return b, nil
	}
	return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
}

func (s *Store) seed() {
	t := time.Now()
	for _, q := range SeedQuotas(t) {
		s.quotas[q.TenantID] = q
	}
	for _, u := range SeedUsages(t) {
		s.usage[u.TenantID] = u
	}
	s.bills = append(s.bills, SeedBills(t)...)
	// seed 占用了序号 1（bill-t-acme-1），后续 GenerateBill 从 2 起，避免 ID 冲突。
	s.seq = 1
}

// SeedQuotas 返回平台预置示例配额，供内存仓储自灌与 PG 仓储迁移后 seed 复用同一真源。
// acme 故意把 GPU 上限调到 4（演示用量超配额告警）；其他资源走 DefaultQuota。
func SeedQuotas(t time.Time) []billing.ResourceQuota {
	acmeQuota := DefaultQuota("t-acme", t)
	acmeQuota.Limits[billing.ResGPU] = 4
	globexQuota := DefaultQuota("t-globex", t)
	return []billing.ResourceQuota{acmeQuota, globexQuota}
}

// SeedUsages 返回平台预置示例用量，供内存仓储自灌与 PG 仓储迁移后 seed 复用同一真源。
// acme GPU 用量 6 故意超其配额 4，演示超限告警。
func SeedUsages(t time.Time) []billing.ResourceUsage {
	return []billing.ResourceUsage{
		{
			TenantID: "t-acme",
			Counts: map[string]int{
				billing.ResApplications: 6,
				billing.ResWorkloads:    14,
				billing.ResModels:       3,
				billing.ResGPU:          6, // 故意超 acme 配额 4，演示超限告警
				billing.ResTokens:       5200,
				billing.ResStorage:      120,
			},
			UpdatedAt: t,
		},
		{
			TenantID: "t-globex",
			Counts: map[string]int{
				billing.ResApplications: 2,
				billing.ResWorkloads:    3,
				billing.ResModels:       1,
				billing.ResGPU:          2,
				billing.ResTokens:       800,
				billing.ResStorage:      40,
			},
			UpdatedAt: t,
		},
	}
}

// SeedBills 返回平台预置示例账单，供内存仓储自灌与 PG 仓储迁移后 seed 复用同一真源。
// acme 一张历史已支付账单（2026-06），演示账单列表与状态机。
func SeedBills(t time.Time) []billing.BillingRecord {
	paidAt := t.Add(-30 * 24 * time.Hour)
	return []billing.BillingRecord{
		{
			ID: "bill-t-acme-1", TenantID: "t-acme", Period: "2026-06",
			Items: []billing.BillItem{
				{Resource: billing.ResApplications, Quantity: 5, UnitPrice: 10.0, Amount: 50.0},
				{Resource: billing.ResWorkloads, Quantity: 12, UnitPrice: 5.0, Amount: 60.0},
				{Resource: billing.ResGPU, Quantity: 4, UnitPrice: 100.0, Amount: 400.0},
			},
			Total:     510.0,
			Status:    billing.StatusPaid,
			CreatedAt: paidAt,
			PaidAt:    &paidAt,
		},
	}
}
