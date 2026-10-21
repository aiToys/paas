// Package memory 提供 billing.Repository 的内存实现，seed 跨两租户示例配额/用量/账单。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/attribution"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 billing.Repository（QuotaStore + UsageStore + BillStore），单 Store 避免重名。
type Store struct {
	mu     sync.RWMutex
	quotas map[string]billing.ResourceQuota // tenantID -> quota
	usage  map[string]billing.ResourceUsage // tenantID -> usage
	bills  []billing.BillingRecord          // 全租户；查询时按 tenant 过滤
	// D5-4 财务深化：预算 / 周期 token 用量 / 供应商对账记录。
	budgets     map[string]billing.Budget        // budgetID -> budget
	tokens      map[string]billing.TokenUsageRow // "tenant|period|app|model" -> row
	settlements []billing.SettlementRecord       // 平台级（人工登记）
	seq         int
}

func NewStore() *Store {
	s := &Store{
		quotas:  map[string]billing.ResourceQuota{},
		usage:   map[string]billing.ResourceUsage{},
		budgets: map[string]billing.Budget{},
		tokens:  map[string]billing.TokenUsageRow{},
	}
	// 不 seed mock 配额/用量/账单（去假数据）：用量从 CheckAndInc 真实派生（应用/工作负载创建时递增），
	// 配额用 GetQuota 默认值（未设返默认非错误），账单由 GenerateBill 真实产生。
	return s
}

// DefaultQuota 返回新租户的默认配额（各资源适度上限）。
func DefaultQuota(tid string, at time.Time) billing.ResourceQuota {
	return billing.ResourceQuota{
		TenantID: tid,
		Limits: map[string]int{
			billing.ResApplications: 50,
			billing.ResWorkloads:    100,
			billing.ResModels:       10,
			billing.ResDataservices: 20,
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

// cloneByApp 深拷贝应用维度归因 map（两层），防外部修改污染内部状态。
func cloneByApp(m map[string]map[string]int) map[string]map[string]int {
	if m == nil {
		return nil
	}
	cp := make(map[string]map[string]int, len(m))
	for app, res := range m {
		cp[app] = cloneIntMap(res)
	}
	return cp
}

// —— Quota ——

func (s *Store) GetQuota(ctx context.Context) (billing.ResourceQuota, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q.TenantID = tid
	q.UpdatedAt = time.Now()
	q.Limits = cloneIntMap(q.Limits) // 写入前深拷贝，隔离 caller 传入的 map
	s.quotas[tid] = q
	q.Limits = cloneIntMap(q.Limits) // 返回前再深拷贝（与 GetQuota 对称），防返回值与 store 内共享同一 map
	return q, nil
}

// ListAllQuotas 跨租户列出全部配额（admin 平台总览，不过滤 tenant；按 TenantID 升序，Limits 深拷贝）。
func (s *Store) ListAllQuotas(ctx context.Context) ([]billing.ResourceQuota, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.ResourceQuota, 0, len(s.quotas))
	for _, q := range s.quotas {
		q.Limits = cloneIntMap(q.Limits) // 返回前深拷贝，与 store 独立
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

// —— Usage ——

func (s *Store) GetUsage(ctx context.Context) (billing.ResourceUsage, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	u.ByApp = cloneByApp(u.ByApp)
	return u, nil
}

// ListAllUsages 跨租户列出全部用量快照（admin 平台总览，不过滤 tenant；按 TenantID 升序，深拷贝）。
func (s *Store) ListAllUsages(ctx context.Context) ([]billing.ResourceUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.ResourceUsage, 0, len(s.usage))
	for _, u := range s.usage {
		u.Counts = cloneIntMap(u.Counts) // 返回前深拷贝，与 store 独立
		u.ByApp = cloneByApp(u.ByApp)
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

func (s *Store) IncUsage(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	appID := attribution.AppFrom(ctx) // 应用级 Key 归因（空=租户级，仅 Counts 聚合）
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[tid]
	if !ok {
		u = billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}}
	}
	newCnt := u.Counts[resource] + delta
	if newCnt < 0 {
		newCnt = 0 // 回滚（delta<0）不夹紧会写负值，污染配额上限校验、高估剩余空间
	}
	u.Counts[resource] = newCnt
	if appID != "" {
		if u.ByApp == nil {
			u.ByApp = map[string]map[string]int{}
		}
		resMap, ok := u.ByApp[appID]
		if !ok {
			resMap = map[string]int{}
			u.ByApp[appID] = resMap
		}
		a := resMap[resource] + delta
		if a < 0 {
			a = 0
		}
		resMap[resource] = a
	}
	u.UpdatedAt = time.Now()
	s.usage[tid] = u
	u.Counts = cloneIntMap(u.Counts) // 返回前深拷贝
	u.ByApp = cloneByApp(u.ByApp)    // 深拷贝防外部污染
	return u, nil
}

// CheckAndInc 原子「检查超限 + 递增」：limit>0 且 usage+delta 超限返 ErrQuotaExceeded（不递增）；
// limit<=0（Unlimited 或未设）不拦截。与 IncUsage 同锁，保证检查-递增原子（横切配额拦截基石）。
func (s *Store) CheckAndInc(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	newCnt := u.Counts[resource] + delta
	if newCnt < 0 {
		newCnt = 0 // 回滚（delta<0）不夹紧会写负值，污染配额上限校验、高估剩余空间
	}
	u.Counts[resource] = newCnt
	u.UpdatedAt = time.Now()
	s.usage[tid] = u
	u.Counts = cloneIntMap(u.Counts)
	return u, nil
}

// —— Bills ——

func (s *Store) ListBills(ctx context.Context) ([]billing.BillingRecord, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.BillingRecord, 0)
	for _, b := range s.bills {
		if b.TenantID == tid {
			out = append(out, cloneBill(b))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListAllBills 跨租户列出全部账单（admin 平台总览，不过滤 tenant；按 TenantID 升序再 CreatedAt 倒序，Items 深拷贝）。
func (s *Store) ListAllBills(ctx context.Context) ([]billing.BillingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.BillingRecord, 0, len(s.bills))
	for _, b := range s.bills {
		out = append(out, cloneBill(b))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) GetBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.bills {
		if b.ID == id && b.TenantID == tid {
			return cloneBill(b), nil
		}
	}
	return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
}

// cloneBill 深拷贝账单（Items 切片底层数组 + PaidAt 指针独立），防止返回值与 store 内部状态
// 共享底层引用引发并发读写 race（与 cloneIntMap 同款防御）。
func cloneBill(b billing.BillingRecord) billing.BillingRecord {
	b.Items = append([]billing.BillItem(nil), b.Items...)
	if b.PaidAt != nil {
		paidAt := *b.PaidAt
		b.PaidAt = &paidAt
	}
	return b
}

// GenerateBill 按当前用量 × 单价生成 period 账单；同 period 已有 unpaid 则覆盖更新。
func (s *Store) GenerateBill(ctx context.Context, period string) (billing.BillingRecord, error) {
	tid, err := tenant.IDOrErr(ctx)
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
			return cloneBill(b), nil // 深拷贝返回，隔离调用方与 store 内部 items 切片
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
	return cloneBill(rec), nil // 深拷贝返回，隔离调用方与 store 内部 items 切片
}

func (s *Store) PayBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := tenant.IDOrErr(ctx)
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
		return cloneBill(b), nil // 深拷贝返回（含 PaidAt 指针），与 store 内独立
	}
	return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
}
