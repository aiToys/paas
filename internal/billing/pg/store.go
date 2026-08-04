// Package pg 提供 billing.Repository 的 PostgreSQL 实现（配额 + 用量 + 账单，单 Store）。
//
// 一个 Store 同时实现三个子接口（QuotaStore/UsageStore/BillStore），与内存版同构
// （方法名沿用接口定义，单 Store 不重名）。显式 WHERE tenant_id 强制多租户过滤；
// Create/SetQuota 以 ctx 租户为准忽略请求体 TenantID；跨租户访问统一 not found（不泄漏存在性）；
// 错误消息沿用内存版领域文本。
//
// JSONB 三种用法（nil 安全由 marshal/unmarshal 辅助保证）：
//   - billing_quotas.limits / billing_usages.counts：map[string]int（marshalIntMap/unmarshalIntMap）。
//   - billing_records.items：[]BillItem（marshalItems/unmarshalItems）。
//
// CheckAndInc（横切配额拦截基石）必须在事务内串行化：
//  1. INSERT ... ON CONFLICT DO NOTHING 预占行（首次无 usage 行）；
//  2. SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE 行锁串行化同租户；
//  3. 查 limit（billing_quotas 无行用 DefaultQuota[resource]）；
//  4. limit>0 且 cur+delta>limit 直接返回 ErrQuotaExceeded（不写，事务回滚）；
//  5. UPDATE counts WHERE tenant_id=$1。
//
// `FOR UPDATE` 行锁保证「检查-递增」原子（与内存版 sync.Mutex 语义等价，并发不漏检、不超写）。
//
// GenerateBill 同 period unpaid 覆盖：`INSERT ... ON CONFLICT (tenant_id, period)
// DO UPDATE SET items/total/status/created_at/paid_at`（重置为 unpaid，覆盖旧 unpaid）。
//
// PayBill 状态机 unpaid -> paid：`UPDATE ... WHERE status='unpaid'`，
// RowsAffected==0 视为已支付或不存在，拒绝重复支付（与内存版「账单已支付」「账单不存在」分支对齐）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/billing/memory"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 实现 billing.Repository（QuotaStore + UsageStore + BillStore），单 Store 避免重名。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 billing PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列常量与各 struct 字段顺序严格对齐（scan 列序必须一致）。
// limits/counts/items 列读取为 []byte，由对应 scan 函数转 nil 安全的 map/slice。
const (
	quotaCols  = `tenant_id, limits, updated_at`
	usageCols  = `tenant_id, counts, updated_at`
	recordCols = `id, tenant_id, period, items, total, status, created_at, paid_at`
)

// ---------- JSONB 辅助（nil 安全） ----------

// marshalIntMap 把 map[string]int 序列化为 JSONB 字节；nil → '{}'（与列 DEFAULT 一致）。
func marshalIntMap(m map[string]int) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// unmarshalIntMap 反序列化 JSONB 为 map[string]int；nil/空/null/无效 → 空 map（非 nil）。
// 保证调用方对返回值直接写入不 panic（与 configcenter.unmarshalSnapshot 同款）。
func unmarshalIntMap(raw []byte) map[string]int {
	m := map[string]int{}
	if len(raw) == 0 {
		return m
	}
	if string(raw) == "null" {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]int{} // 容错：单行坏数据不阻塞整个 List
	}
	return m
}

// marshalItems 把 []BillItem 序列化为 JSONB 字节；nil → '[]'（与列 DEFAULT 一致）。
func marshalItems(items []billing.BillItem) ([]byte, error) {
	if items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(items)
}

// unmarshalItems 反序列化 JSONB 为 []BillItem；nil/空/null/无效 → 空 slice（非 nil）。
func unmarshalItems(raw []byte) []billing.BillItem {
	items := []billing.BillItem{}
	if len(raw) == 0 {
		return items
	}
	if string(raw) == "null" {
		return items
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return []billing.BillItem{}
	}
	return items
}

// ---------- scan 辅助 ----------

func scanQuota(r storagepg.RowScanner, q *billing.ResourceQuota) error {
	var limitsRaw []byte
	if err := r.Scan(&q.TenantID, &limitsRaw, &q.UpdatedAt); err != nil {
		return err
	}
	q.Limits = unmarshalIntMap(limitsRaw)
	return nil
}

func scanUsage(r storagepg.RowScanner, u *billing.ResourceUsage) error {
	var countsRaw []byte
	if err := r.Scan(&u.TenantID, &countsRaw, &u.UpdatedAt); err != nil {
		return err
	}
	u.Counts = unmarshalIntMap(countsRaw)
	return nil
}

func scanRecord(r storagepg.RowScanner, b *billing.BillingRecord) error {
	var itemsRaw []byte
	if err := r.Scan(&b.ID, &b.TenantID, &b.Period, &itemsRaw, &b.Total, &b.Status, &b.CreatedAt, &b.PaidAt); err != nil {
		return err
	}
	b.Items = unmarshalItems(itemsRaw)
	return nil
}

// ---------- QuotaStore ----------

// GetQuota 读取配额；无行返回 DefaultQuota（非错误，与内存版一致）。
func (s *Store) GetQuota(ctx context.Context) (billing.ResourceQuota, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+quotaCols+` FROM billing_quotas WHERE tenant_id=$1`, tid)
	var q billing.ResourceQuota
	if err = scanQuota(row, &q); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return memory.DefaultQuota(tid, time.Now()), nil
		}
		return billing.ResourceQuota{}, err
	}
	return q, nil
}

// SetQuota 覆盖更新配额（INSERT ... ON CONFLICT DO UPDATE）。
// 以 ctx 租户为准忽略请求体 TenantID；返回值含实际落库的 updated_at。
func (s *Store) SetQuota(ctx context.Context, q billing.ResourceQuota) (billing.ResourceQuota, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	q.TenantID = tid
	q.UpdatedAt = time.Now()
	limitsBytes, err := marshalIntMap(q.Limits)
	if err != nil {
		return billing.ResourceQuota{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO billing_quotas (`+quotaCols+`)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id) DO UPDATE
    SET limits     = EXCLUDED.limits,
        updated_at = EXCLUDED.updated_at
RETURNING `+quotaCols,
		q.TenantID, limitsBytes, q.UpdatedAt)
	var saved billing.ResourceQuota
	if err = scanQuota(row, &saved); err != nil {
		return billing.ResourceQuota{}, err
	}
	return saved, nil
}

// ListAllQuotas 跨租户列出全部配额（admin 平台总览，不过滤 tenant；按 tenant_id 排序）。
func (s *Store) ListAllQuotas(ctx context.Context) ([]billing.ResourceQuota, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+quotaCols+` FROM billing_quotas ORDER BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]billing.ResourceQuota, 0)
	for rows.Next() {
		var q billing.ResourceQuota
		if err = scanQuota(rows, &q); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ---------- UsageStore ----------

// GetUsage 读取用量；无行返回空 Counts（非错误，与内存版一致）。
func (s *Store) GetUsage(ctx context.Context) (billing.ResourceUsage, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+usageCols+` FROM billing_usages WHERE tenant_id=$1`, tid)
	var u billing.ResourceUsage
	if err = scanUsage(row, &u); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return billing.ResourceUsage{TenantID: tid, Counts: map[string]int{}, UpdatedAt: time.Now()}, nil
		}
		return billing.ResourceUsage{}, err
	}
	return u, nil
}

// IncUsage 增减某资源用量（delta 可负）。无 usage 行时自动建行；不检查配额上限
// （横切拦截由 CheckAndInc 承担，IncUsage 仅演示/手动调整用）。
func (s *Store) IncUsage(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 预占行（首次无 usage 行）。
	if _, err = tx.Exec(ctx,
		`INSERT INTO billing_usages (tenant_id, counts, updated_at) VALUES ($1, '{}', now()) ON CONFLICT (tenant_id) DO NOTHING`,
		tid); err != nil {
		return billing.ResourceUsage{}, err
	}
	// 行锁串行化同租户并发（与 CheckAndInc 同款，保证读-改-写原子）。
	var countsRaw []byte
	if err = tx.QueryRow(ctx,
		`SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE`, tid).Scan(&countsRaw); err != nil {
		return billing.ResourceUsage{}, err
	}
	counts := unmarshalIntMap(countsRaw)
	counts[resource] += delta
	if counts[resource] < 0 {
		counts[resource] = 0 // 夹紧负值，与内存版语义一致，防配额检查误判绕过
	}
	countsBytes, err := marshalIntMap(counts)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	var u billing.ResourceUsage
	row := tx.QueryRow(ctx,
		`UPDATE billing_usages SET counts=$2, updated_at=now() WHERE tenant_id=$1 RETURNING `+usageCols,
		tid, countsBytes)
	if err = scanUsage(row, &u); err != nil {
		return billing.ResourceUsage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return billing.ResourceUsage{}, err
	}
	return u, nil
}

// CheckAndInc 原子「检查超限 + 递增」（横切配额拦截基石）。
//
// 事务内 SELECT FOR UPDATE 串行化同租户并发：limit>0 且 cur+delta>limit 返 ErrQuotaExceeded
// （不写，事务回滚）；limit<=0（Unlimited 或未设）不拦截，照常递增。
// 与内存版 sync.Mutex 语义等价：并发请求中只有 limit 内的请求成功，超限全部拒绝。
//
// limit 查询：billing_quotas 有行用 limits[resource]；无行（或 limits 无该 key）用
// DefaultQuota[resource]。limits[key]==0 视为未设 → Unlimited（与内存版一致）。
func (s *Store) CheckAndInc(ctx context.Context, resource string, delta int) (billing.ResourceUsage, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害

	// 1) 预占行（首次无 usage 行）。
	if _, err = tx.Exec(ctx,
		`INSERT INTO billing_usages (tenant_id, counts, updated_at) VALUES ($1, '{}', now()) ON CONFLICT (tenant_id) DO NOTHING`,
		tid); err != nil {
		return billing.ResourceUsage{}, err
	}

	// 2) 行锁串行化同租户并发。
	var countsRaw []byte
	if err = tx.QueryRow(ctx,
		`SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE`, tid).Scan(&countsRaw); err != nil {
		return billing.ResourceUsage{}, err
	}
	counts := unmarshalIntMap(countsRaw)

	// 3) 查 limit：billing_quotas 无行用 DefaultQuota。
	// 注：必须用 tx（而非 s.db.Pool()）查询，复用事务占用的同一连接；否则并发事务会从池中
	// 再借一连接，连接池耗尽时死锁（N 个事务各持 1 conn 等 第 2 conn，无 conn 释放）。
	limit, err := limitForLockedTx(ctx, tx, tid, resource)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	cur := counts[resource]
	if limit > 0 && cur+delta > limit {
		// 不写，事务回滚（defer Rollback）；返回与内存版同款错误文本。
		return billing.ResourceUsage{}, fmt.Errorf("%w: %s（上限 %d，当前 %d，请求 %+d）",
			billing.ErrQuotaExceeded, resource, limit, cur, delta)
	}

	// 4) 递增并落库。
	counts[resource] = cur + delta
	if counts[resource] < 0 {
		counts[resource] = 0 // 夹紧负值（删除回滚/seed 偏差致计数为负时），防后续 CheckAndInc 误判绕过
	}
	countsBytes, err := marshalIntMap(counts)
	if err != nil {
		return billing.ResourceUsage{}, err
	}
	var u billing.ResourceUsage
	row := tx.QueryRow(ctx,
		`UPDATE billing_usages SET counts=$2, updated_at=now() WHERE tenant_id=$1 RETURNING `+usageCols,
		tid, countsBytes)
	if err = scanUsage(row, &u); err != nil {
		return billing.ResourceUsage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return billing.ResourceUsage{}, err
	}
	return u, nil
}

// limitForLockedTx 在已加 billing_usages 行锁的事务内查询某资源的配额上限。
// billing_quotas 无行 → 用 DefaultQuota[resource]；有行但 limits 无该 key 或 ==0 → Unlimited。
//
// 必须复用传入的 tx（同一连接），不能用 s.db.Pool().QueryRow：
// CheckAndInc 已通过 Begin 占用一连接并对 billing_usages 加 FOR UPDATE 行锁；
// 若此处再从连接池借另一连接查 billing_quotas，N 个并发事务各持 1 conn 等第 2 conn，
// 池子打满时（默认 max conns 通常远小于并发数）全体死锁。
// 复用 tx 在同一连接上完成配额查询，无二次借连接，不引入新的锁竞争。
//
// billing_quotas 与 billing_usages 是不同表的独立行，FOR UPDATE 不会跨界升级为表锁，
// 配额查询走 tx 不会与自身持有的 usage 行锁冲突。
func limitForLockedTx(ctx context.Context, tx pgx.Tx, tid, resource string) (int, error) {
	row := tx.QueryRow(ctx, `SELECT limits FROM billing_quotas WHERE tenant_id=$1`, tid)
	var limitsRaw []byte
	if err := row.Scan(&limitsRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 无配额行 → 默认配额；默认值不含资源视为 0（无限）。
			return memory.DefaultQuota(tid, time.Now()).Limits[resource], nil
		}
		return 0, err
	}
	limits := unmarshalIntMap(limitsRaw)
	v, ok := limits[resource]
	if !ok || v == 0 {
		return billing.Unlimited, nil
	}
	return v, nil
}

// ---------- BillStore ----------

// ListBills 账单列表，按 CreatedAt 倒序（与内存版一致）。
func (s *Store) ListBills(ctx context.Context) ([]billing.BillingRecord, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+recordCols+` FROM billing_records WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]billing.BillingRecord, 0)
	for rows.Next() {
		var b billing.BillingRecord
		if err = scanRecord(rows, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListAllBills 跨租户列出全部账单（admin 平台总览，不过滤 tenant；按 tenant_id, created_at DESC 排序）。
func (s *Store) ListAllBills(ctx context.Context) ([]billing.BillingRecord, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+recordCols+` FROM billing_records ORDER BY tenant_id, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]billing.BillingRecord, 0)
	for rows.Next() {
		var b billing.BillingRecord
		if err = scanRecord(rows, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBill 读单条账单；跨租户访问 not found（不泄漏存在性）。
func (s *Store) GetBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+recordCols+` FROM billing_records WHERE id=$1 AND tenant_id=$2`, id, tid)
	var b billing.BillingRecord
	if err = scanRecord(row, &b); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
		}
		return billing.BillingRecord{}, err
	}
	return b, nil
}

// GenerateBill 按当前用量 × 单价生成 period 账单；同 period 已有 unpaid 则覆盖
// （INSERT ... ON CONFLICT (tenant_id, period) DO UPDATE ... WHERE status='unpaid'）。
//
// 已 paid 的账单不覆盖（与内存版「仅覆盖 unpaid」语义对齐）：ON CONFLICT 的 WHERE 不满足时
// DO UPDATE 不执行，RETURNING 返回 0 行（pgx ErrNoRows），此时回退到 getBillByPeriod 返回
// 现存 paid 记录（金额/paid_at/状态保持不变）。
func (s *Store) GenerateBill(ctx context.Context, period string) (billing.BillingRecord, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	if err := billing.ValidatePeriod(period); err != nil {
		return billing.BillingRecord{}, err
	}

	// 取当前用量（无 usage 行视作空 Counts）。
	u, err := s.GetUsage(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}

	// 按 ResourceOrder 算明细 + 总额（与内存版逐项遍历一致，qty<=0 跳过）。
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

	id := newBillID(tid)
	itemsBytes, err := marshalItems(items)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	rec := billing.BillingRecord{
		ID:        id,
		TenantID:  tid,
		Period:    period,
		Items:     items,
		Total:     total,
		Status:    billing.StatusUnpaid,
		CreatedAt: time.Now(),
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO billing_records (id, tenant_id, period, items, total, status, created_at, paid_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)
ON CONFLICT (tenant_id, period) DO UPDATE
    SET items      = EXCLUDED.items,
        total      = EXCLUDED.total,
        status     = EXCLUDED.status,
        created_at = EXCLUDED.created_at,
        paid_at    = EXCLUDED.paid_at
    WHERE billing_records.status = 'unpaid'
RETURNING `+recordCols,
		rec.ID, rec.TenantID, rec.Period, itemsBytes, rec.Total, rec.Status, rec.CreatedAt)
	var saved billing.BillingRecord
	if err = scanRecord(row, &saved); err != nil {
		// 冲突且现存 status='paid'（WHERE 不满足，DO UPDATE 不执行）→ RETURNING 0 行。
		// 与内存版「已 paid 不覆盖」语义对齐：回退查现存 paid 记录原样返回。
		if errors.Is(err, pgx.ErrNoRows) {
			return s.getBillByPeriod(ctx, rec.TenantID, period)
		}
		return billing.BillingRecord{}, err
	}
	return saved, nil
}

// getBillByPeriod 按 (tenant, period) 取现存账单（GenerateBill 不覆盖 paid 时回退用）。
// 调用方已校验 tid；此处直接用传入 tid 不再走 TenantOrErr，避免重复 ctx 解析。
func (s *Store) getBillByPeriod(ctx context.Context, tid, period string) (billing.BillingRecord, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+recordCols+` FROM billing_records WHERE tenant_id=$1 AND period=$2`, tid, period)
	var b billing.BillingRecord
	if err := scanRecord(row, &b); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 理论不可达：ON CONFLICT 路径的 ErrNoRows 仅在冲突行存在时出现。
			return billing.BillingRecord{}, fmt.Errorf("账单不存在: %s/%s", tid, period)
		}
		return billing.BillingRecord{}, err
	}
	return b, nil
}

// PayBill 支付账单（unpaid -> paid）。已支付或不存在报错（与内存版分支一致）：
// RowsAffected==0 时按「账单已支付: id」或「账单不存在: id」分别报错（先查 status 决定）。
func (s *Store) PayBill(ctx context.Context, id string) (billing.BillingRecord, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE billing_records SET status=$3, paid_at=now() WHERE id=$1 AND tenant_id=$2 AND status=$4`,
		id, tid, billing.StatusPaid, billing.StatusUnpaid)
	if err != nil {
		return billing.BillingRecord{}, err
	}
	if tag.RowsAffected() == 0 {
		// 区分：存在但已支付 / 完全不存在（含跨租户）。
		existing, gerr := s.GetBill(ctx, id)
		if gerr != nil {
			// 跨租户或不存在 → 沿用 GetBill 的「账单不存在」文本。
			return billing.BillingRecord{}, gerr
		}
		if existing.Status == billing.StatusPaid {
			return billing.BillingRecord{}, fmt.Errorf("账单已支付: %s", id)
		}
		// 兜底（理论上不会到这）。
		return billing.BillingRecord{}, fmt.Errorf("账单状态异常: %s", id)
	}
	return s.GetBill(ctx, id)
}

// ---------- Count 方法（供 PG seed 判空，表空才灌，幂等；不经租户过滤，仅启动期用） ----------

// QuotasCount 返回 billing_quotas 全表行数。
func (s *Store) QuotasCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM billing_quotas`).Scan(&n)
	return n, err
}

// UsagesCount 返回 billing_usages 全表行数。
func (s *Store) UsagesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM billing_usages`).Scan(&n)
	return n, err
}

// RecordsCount 返回 billing_records 全表行数。
func (s *Store) RecordsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM billing_records`).Scan(&n)
	return n, err
}

// SeedIfEmpty 在表空时灌入 seed 真源（启动期用，幂等；不依赖 Create 编排）。
// 直接 SQL INSERT 绕过 GenerateBill/PayBill 状态机，保留历史 paid 账单与超限示例用量。
// quotas/usages 主键为 tenant_id，records 主键为 id + UNIQUE(tenant,period)，
// 任意已存在则 ON CONFLICT DO NOTHING 跳过（多租户 seed 全表一次灌完，无需 ctx 租户）。
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	// 去假数据：不灌 mock 配额/用量/账单。配额用 GetQuota 默认值（未设返默认非错误），
	// 用量从 CheckAndInc 真实派生（应用/工作负载创建时递增），账单由 GenerateBill 真实产生。
	// 保留签名兼容 seedPGAllIfEmpty 调用。
	return nil
}

// 编译期断言：Store 实现全部三子接口（类型不匹配时编译失败）。
var (
	_ billing.QuotaStore = (*Store)(nil)
	_ billing.UsageStore = (*Store)(nil)
	_ billing.BillStore  = (*Store)(nil)
)

// newBillID 生成带前缀的短 ID（纳秒时间戳 + 前缀）。mock 期保证基本唯一，与 configcenter PG 同款风格。
func newBillID(tid string) string {
	return fmt.Sprintf("bill-%s-%d", tid, time.Now().UnixNano())
}
