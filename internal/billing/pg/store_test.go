//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `PAAS_TEST_PG_URL=... make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 billing 3 表）避免残留。
//
// 测试覆盖：
//   - GetQuota 无行返 DefaultQuota（非错误）；SetQuota 覆盖（INSERT/ON CONFLICT 两路径）。
//   - GetUsage 无行返空 Counts（非错误）。
//   - IncUsage 增减 + 多次累计 + 无行自动建行。
//   - CheckAndInc 正常递增 / 超限返 ErrQuotaExceeded 不写 / Unlimited(-1) 不拦截 / 未设 limit 不拦截。
//   - CheckAndInc 并发安全：50 goroutine 同资源 +1，最终 count==50（-race 无报告）。
//   - CheckAndInc 跨租户独立计数（隔离）。
//   - GenerateBill 同 period 覆盖（UNIQUE 命中 ON CONFLICT DO UPDATE）。
//   - PayBill 状态机 unpaid -> paid；重复支付拒绝；跨租户 not found。
//   - 租户隔离 + 缺失租户拒绝。

package pg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/billing"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

// resetSchema 清空所有业务表 + 迁移版本表，避免跨包测试残留污染。
// 覆盖全部已迁模块表（含 billing 3 表）。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS billing_records, billing_usages, billing_quotas,
					cc_publishes, cc_items, cc_namespaces,
					gov_breakers, gov_routes, gov_instances, gov_services,
					releases, images, build_runs, code_repos,
					workloads, data_services, appconfigs, environments,
					application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// ---------- QuotaStore ----------

func TestGetQuotaMissingRowReturnsDefault(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	q, err := s.GetQuota(ctx)
	if err != nil {
		t.Fatalf("GetQuota 无行应返回默认配额非错误, got %v", err)
	}
	if q.TenantID != "t-acme" {
		t.Fatalf("TenantID 应 = t-acme, got %s", q.TenantID)
	}
	// 默认配额 applications=50、tokens=-1（无限）。
	if q.Limits[billing.ResApplications] != 50 {
		t.Fatalf("默认 applications 配额应 50, got %d", q.Limits[billing.ResApplications])
	}
	if q.Limits[billing.ResTokens] != billing.Unlimited {
		t.Fatalf("默认 tokens 应 Unlimited(-1), got %d", q.Limits[billing.ResTokens])
	}
	if q.Limits == nil {
		t.Fatalf("Limits 不应 nil")
	}
}

func TestSetQuotaInsertAndOverwrite(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 首次 SetQuota：INSERT 路径。
	q1 := billing.ResourceQuota{Limits: map[string]int{
		billing.ResApplications: 10,
		billing.ResWorkloads:    billing.Unlimited,
	}}
	saved, err := s.SetQuota(ctx, q1)
	if err != nil {
		t.Fatalf("SetQuota #1: %v", err)
	}
	if saved.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", saved.TenantID)
	}
	if saved.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt 应由 store 填充")
	}
	if saved.Limits[billing.ResApplications] != 10 {
		t.Fatalf("applications 应 10, got %d", saved.Limits[billing.ResApplications])
	}

	// 二次 SetQuota：ON CONFLICT 覆盖（同一 tenant_id 仅一行）。
	q2 := billing.ResourceQuota{Limits: map[string]int{
		billing.ResApplications: 99,
	}}
	saved2, err := s.SetQuota(ctx, q2)
	if err != nil {
		t.Fatalf("SetQuota #2: %v", err)
	}
	if saved2.Limits[billing.ResApplications] != 99 {
		t.Fatalf("覆盖后 applications 应 99, got %d", saved2.Limits[billing.ResApplications])
	}
	// 旧 key 应被整体覆盖（Limits 是 map 全量替换，非 merge）。
	if _, ok := saved2.Limits[billing.ResWorkloads]; ok {
		t.Fatalf("覆盖后 workloads 应不存在（map 全量替换）")
	}

	// 表内应仅 1 行（同 tenant_id PK）。
	if n, _ := s.QuotasCount(ctx); n != 1 {
		t.Fatalf("QuotasCount 应 1, got %d", n)
	}

	// GetQuota 往返一致。
	got, _ := s.GetQuota(ctx)
	if got.Limits[billing.ResApplications] != 99 {
		t.Fatalf("GetQuota 往返不一致: %v", got.Limits)
	}
}

// ---------- UsageStore ----------

func TestGetUsageMissingRowReturnsEmpty(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	u, err := s.GetUsage(ctx)
	if err != nil {
		t.Fatalf("GetUsage 无行应返回空 Counts 非错误, got %v", err)
	}
	if u.Counts == nil {
		t.Fatalf("Counts 不应 nil")
	}
	if len(u.Counts) != 0 {
		t.Fatalf("Counts 应空, got %v", u.Counts)
	}
}

func TestIncUsageAccumulate(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 首次 IncUsage：无 usage 行，自动建行。
	u, err := s.IncUsage(ctx, billing.ResApplications, 5)
	if err != nil {
		t.Fatalf("IncUsage #1: %v", err)
	}
	if u.Counts[billing.ResApplications] != 5 {
		t.Fatalf("applications 应 5, got %d", u.Counts[billing.ResApplications])
	}
	// 二次：累计。
	u2, _ := s.IncUsage(ctx, billing.ResApplications, 3)
	if u2.Counts[billing.ResApplications] != 8 {
		t.Fatalf("累计 applications 应 8, got %d", u2.Counts[billing.ResApplications])
	}
	// delta 负：减回。
	u3, _ := s.IncUsage(ctx, billing.ResApplications, -2)
	if u3.Counts[billing.ResApplications] != 6 {
		t.Fatalf("减后 applications 应 6, got %d", u3.Counts[billing.ResApplications])
	}
	// 另一资源：并存。
	u4, _ := s.IncUsage(ctx, billing.ResWorkloads, 4)
	if u4.Counts[billing.ResWorkloads] != 4 {
		t.Fatalf("workloads 应 4, got %d", u4.Counts[billing.ResWorkloads])
	}
	if u4.Counts[billing.ResApplications] != 6 {
		t.Fatalf("applications 应保持 6, got %d", u4.Counts[billing.ResApplications])
	}
}

// ---------- CheckAndInc ----------

func TestCheckAndIncNormal(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 设配额 workloads=10，CheckAndInc +5 正常。
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResWorkloads: 10}})
	u, err := s.CheckAndInc(ctx, billing.ResWorkloads, 5)
	if err != nil {
		t.Fatalf("CheckAndInc 正常递增不应报错: %v", err)
	}
	if u.Counts[billing.ResWorkloads] != 5 {
		t.Fatalf("workloads 应 5, got %d", u.Counts[billing.ResWorkloads])
	}
}

func TestCheckAndIncExceedsLimit(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 配额 workloads=3。
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResWorkloads: 3}})

	// +3 OK（恰好上限）。
	if _, err := s.CheckAndInc(ctx, billing.ResWorkloads, 3); err != nil {
		t.Fatalf("恰好到上限应 OK: %v", err)
	}
	// +1 超限：返 ErrQuotaExceeded 且不写。
	_, err := s.CheckAndInc(ctx, billing.ResWorkloads, 1)
	if !errors.Is(err, billing.ErrQuotaExceeded) {
		t.Fatalf("超限应返 ErrQuotaExceeded, got %v", err)
	}
	// 校验用量未变（仍 3）。
	u, _ := s.GetUsage(ctx)
	if u.Counts[billing.ResWorkloads] != 3 {
		t.Fatalf("超限不应写, workloads 应保持 3, got %d", u.Counts[billing.ResWorkloads])
	}

	// delta 负超限：从 3 减到 -1 不会超 limit（limit 管上限不管下限），但若 delta 大正导致超限也拒。
	// -5 不超上限，应通过（用 workloads=3 减 5 → -2）。
	if _, err := s.CheckAndInc(ctx, billing.ResWorkloads, -5); err != nil {
		t.Fatalf("负 delta 不超上限应通过, got %v", err)
	}
}

func TestCheckAndIncUnlimitedNotBlocked(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 配额 workloads=-1（Unlimited）：任意大 delta 不拦截。
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResWorkloads: billing.Unlimited}})
	if _, err := s.CheckAndInc(ctx, billing.ResWorkloads, 100000); err != nil {
		t.Fatalf("Unlimited 应不拦截, got %v", err)
	}
	u, _ := s.GetUsage(ctx)
	if u.Counts[billing.ResWorkloads] != 100000 {
		t.Fatalf("Unlimited 应写满, got %d", u.Counts[billing.ResWorkloads])
	}
}

func TestCheckAndIncMissingLimitNotBlocked(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 完全不设配额行 → 走 DefaultQuota（workloads=100，非无限）。
	// 这里测的是「limits 无该 key → Unlimited」分支：SetQuota 设其他资源，不设 workloads。
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResApplications: 50}})
	if _, err := s.CheckAndInc(ctx, billing.ResWorkloads, 9999); err != nil {
		t.Fatalf("limits 无 workloads key 应视为 Unlimited 不拦截, got %v", err)
	}
	u, _ := s.GetUsage(ctx)
	if u.Counts[billing.ResWorkloads] != 9999 {
		t.Fatalf("workloads 应写 9999, got %d", u.Counts[billing.ResWorkloads])
	}
}

// TestCheckAndIncConcurrentSafety 验证 CheckAndInc 的并发安全：
// 50 goroutine 同时 +1 workloads（配额上限 100），全部应成功（无超限），最终 count==50。
// -race 运行时还应检测无数据竞争（go test -race 全局生效）。
//
// 反向用例（TestCheckAndIncConcurrentRolloverExceeds）：
// 配额上限 = 30，50 个 +1 中只能成功 30 个，超限 20 个返 ErrQuotaExceeded；最终 count==30。
// 这两个用例共同验证「不漏检、不超写」。
func TestCheckAndIncConcurrentSafety(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	const limit = 100
	const n = 50
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResWorkloads: limit}})

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = s.CheckAndInc(ctx, billing.ResWorkloads, 1)
		}(i)
	}
	wg.Wait()

	// 50 全部应成功（远未超 limit=100）。
	success := 0
	for _, e := range errs {
		if e == nil {
			success++
		}
	}
	if success != n {
		t.Fatalf("并发 %d 个 +1 应全部成功（limit=%d），实际成功 %d", n, limit, success)
	}
	// 最终用量精确 = 50（不多写、不少写）。
	u, _ := s.GetUsage(ctx)
	if u.Counts[billing.ResWorkloads] != n {
		t.Fatalf("并发后 workloads 应 = %d, got %d", n, u.Counts[billing.ResWorkloads])
	}
}

func TestCheckAndIncConcurrentRolloverExceeds(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	const limit = 30
	const n = 50
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{billing.ResWorkloads: limit}})

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = s.CheckAndInc(ctx, billing.ResWorkloads, 1)
		}(i)
	}
	wg.Wait()

	// 成功数应恰好 = limit（30），其余 20 个返 ErrQuotaExceeded。
	success := 0
	exceeded := 0
	for _, e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, billing.ErrQuotaExceeded) {
			exceeded++
		} else {
			t.Fatalf("意外错误: %v", e)
		}
	}
	if success != limit {
		t.Fatalf("并发成功数应 = limit(%d), got %d", limit, success)
	}
	if exceeded != n-limit {
		t.Fatalf("超限数应 = %d, got %d", n-limit, exceeded)
	}
	// 最终用量精确 = limit（不超写）。
	u, _ := s.GetUsage(ctx)
	if u.Counts[billing.ResWorkloads] != limit {
		t.Fatalf("并发后 workloads 应恰好 = limit(%d), got %d", limit, u.Counts[billing.ResWorkloads])
	}
}

// ---------- BillStore ----------

func TestGenerateBillOverwriteSamePeriod(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 灌点用量：2 applications（单价 10）、3 workloads（单价 5）。
	s.IncUsage(ctx, billing.ResApplications, 2)
	s.IncUsage(ctx, billing.ResWorkloads, 3)

	// 首次生成 2026-07 账单。
	rec1, err := s.GenerateBill(ctx, "2026-07")
	if err != nil {
		t.Fatalf("GenerateBill #1: %v", err)
	}
	if rec1.Status != billing.StatusUnpaid {
		t.Fatalf("应 unpaid, got %s", rec1.Status)
	}
	// total = 2*10 + 3*5 = 35。
	if rec1.Total != 35.0 {
		t.Fatalf("Total 应 35.0, got %v", rec1.Total)
	}
	if len(rec1.Items) != 2 {
		t.Fatalf("Items 应 2 项, got %d", len(rec1.Items))
	}
	if rec1.PaidAt != nil {
		t.Fatalf(" unpaid PaidAt 应 nil")
	}

	// 增加用量后再生成同 period：覆盖（同 period 仅一行，total 更新）。
	s.IncUsage(ctx, billing.ResWorkloads, 7) // workloads 累计 10
	rec2, err := s.GenerateBill(ctx, "2026-07")
	if err != nil {
		t.Fatalf("GenerateBill #2: %v", err)
	}
	// total = 2*10 + 10*5 = 70。
	if rec2.Total != 70.0 {
		t.Fatalf("覆盖后 Total 应 70.0, got %v", rec2.Total)
	}
	// 表内应仅 1 行（UNIQUE(tenant, period)）。
	if n, _ := s.RecordsCount(ctx); n != 1 {
		t.Fatalf("同 period 应仅 1 行, RecordsCount=%d", n)
	}

	// ListBills 单条。
	list, _ := s.ListBills(ctx)
	if len(list) != 1 {
		t.Fatalf("ListBills 应 1 条, got %d", len(list))
	}
}

func TestGenerateBillInvalidPeriod(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	_, err := s.GenerateBill(ctx, "2026-13") // 非法月份
	if err == nil {
		t.Fatalf("非法 period 应报错")
	}
}

// TestGenerateBillPaidNotOverwritten 验证已支付账单不被 GenerateBill 覆盖回 unpaid
// （与内存版「仅覆盖 unpaid」语义对齐，PG 用 ON CONFLICT WHERE status='unpaid' 实现）。
func TestGenerateBillPaidNotOverwritten(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 灌点用量生成 2026-08 账单 + 支付。
	s.IncUsage(ctx, billing.ResApplications, 2) // 2*10=20
	rec, err := s.GenerateBill(ctx, "2026-08")
	if err != nil {
		t.Fatalf("GenerateBill #1: %v", err)
	}
	if rec.Total != 20.0 {
		t.Fatalf("初始 Total 应 20.0, got %v", rec.Total)
	}
	paid, err := s.PayBill(ctx, rec.ID)
	if err != nil {
		t.Fatalf("PayBill: %v", err)
	}
	if paid.Status != billing.StatusPaid {
		t.Fatalf("应 paid, got %s", paid.Status)
	}
	origPaidAt := paid.PaidAt
	origID := paid.ID
	if origPaidAt == nil {
		t.Fatalf("PaidAt 应填充")
	}

	// 用量增加后再次生成同 period：已 paid，不覆盖。
	s.IncUsage(ctx, billing.ResApplications, 8) // 总 10*10=100
	rec2, err := s.GenerateBill(ctx, "2026-08")
	if err != nil {
		t.Fatalf("GenerateBill #2（已 paid 不覆盖）: %v", err)
	}
	// 返回的是现存 paid 记录（金额/状态/paid_at/id 保持不变）。
	if rec2.Status != billing.StatusPaid {
		t.Fatalf("已 paid 不应被覆盖回 unpaid, got %s", rec2.Status)
	}
	if rec2.Total != 20.0 {
		t.Fatalf("金额应保持 20.0（不重算）, got %v", rec2.Total)
	}
	if rec2.ID != origID {
		t.Fatalf("ID 应保持 %s, got %s", origID, rec2.ID)
	}
	if rec2.PaidAt == nil || !rec2.PaidAt.Equal(*origPaidAt) {
		t.Fatalf("PaidAt 应保持原值")
	}

	// 表内仍 1 行，金额未变。
	list, _ := s.ListBills(ctx)
	if len(list) != 1 {
		t.Fatalf("同 period 应仅 1 行, got %d", len(list))
	}
	if list[0].Total != 20.0 {
		t.Fatalf("落库金额应保持 20.0, got %v", list[0].Total)
	}
}

func TestPayBillStateMachine(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	s.IncUsage(ctx, billing.ResApplications, 1)
	rec, _ := s.GenerateBill(ctx, "2026-07")

	if rec.Status != billing.StatusUnpaid {
		t.Fatalf("初始应 unpaid, got %s", rec.Status)
	}

	// 支付：unpaid -> paid。
	paid, err := s.PayBill(ctx, rec.ID)
	if err != nil {
		t.Fatalf("PayBill: %v", err)
	}
	if paid.Status != billing.StatusPaid {
		t.Fatalf("应 paid, got %s", paid.Status)
	}
	if paid.PaidAt == nil {
		t.Fatalf("PaidAt 应填充")
	}

	// 重复支付：拒绝。
	_, err = s.PayBill(ctx, rec.ID)
	if err == nil || !strings.Contains(err.Error(), "账单已支付") {
		t.Fatalf("重复支付应报「账单已支付」, got %v", err)
	}
}

func TestPayBillMissing(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	_, err := s.PayBill(ctx, "no-such-bill")
	if err == nil || !strings.Contains(err.Error(), "账单不存在") {
		t.Fatalf("缺失账单应报「账单不存在」, got %v", err)
	}
}

func TestGetBillCrossTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	s.IncUsage(ctx, billing.ResApplications, 1)
	rec, _ := s.GenerateBill(ctx, "2026-07")

	// Globex 读 Acme 账单：not found（不泄漏）。
	if _, err := s.GetBill(globexCtx(), rec.ID); err == nil ||
		!strings.Contains(err.Error(), "账单不存在") {
		t.Fatalf("跨租户 GetBill 应 not found, got %v", err)
	}
	// Globex 支付 Acme 账单：拒绝（同款 not found）。
	if _, err := s.PayBill(globexCtx(), rec.ID); err == nil ||
		!strings.Contains(err.Error(), "账单不存在") {
		t.Fatalf("跨租户 PayBill 应 not found, got %v", err)
	}
}

// ---------- 多租户隔离 ----------

func TestTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	acme := acmeCtx()
	globex := globexCtx()

	// 各设各的配额（互不影响）。
	s.SetQuota(acme, billing.ResourceQuota{Limits: map[string]int{billing.ResApplications: 10}})
	s.SetQuota(globex, billing.ResourceQuota{Limits: map[string]int{billing.ResApplications: 5}})

	// Acme 用量 4，Globex 用量 3。
	s.IncUsage(acme, billing.ResApplications, 4)
	s.IncUsage(globex, billing.ResApplications, 3)

	// Acme 视角：4/10；Globex 视角：3/5（隔离）。
	uA, _ := s.GetUsage(acme)
	uG, _ := s.GetUsage(globex)
	if uA.Counts[billing.ResApplications] != 4 {
		t.Fatalf("Acme 应 4, got %d", uA.Counts[billing.ResApplications])
	}
	if uG.Counts[billing.ResApplications] != 3 {
		t.Fatalf("Globex 应 3, got %d", uG.Counts[billing.ResApplications])
	}
	qA, _ := s.GetQuota(acme)
	qG, _ := s.GetQuota(globex)
	if qA.Limits[billing.ResApplications] != 10 {
		t.Fatalf("Acme 配额应 10, got %d", qA.Limits[billing.ResApplications])
	}
	if qG.Limits[billing.ResApplications] != 5 {
		t.Fatalf("Globex 配额应 5, got %d", qG.Limits[billing.ResApplications])
	}

	// CheckAndInc 跨租户独立：Acme 配额 10 还剩 6（4 已用），+5 OK。
	if _, err := s.CheckAndInc(acme, billing.ResApplications, 5); err != nil {
		t.Fatalf("Acme +5 应 OK（4+5=9 <= 10）, got %v", err)
	}
	// Globex 配额 5 已用 3，+5 超限（3+5=8 > 5）。
	if _, err := s.CheckAndInc(globex, billing.ResApplications, 5); !errors.Is(err, billing.ErrQuotaExceeded) {
		t.Fatalf("Globex +5 应超限, got %v", err)
	}

	// 账单隔离：Globex ListBills 不见 Acme 的。
	s.GenerateBill(acme, "2026-07")
	if list, _ := s.ListBills(globex); len(list) != 0 {
		t.Fatalf("跨租户 ListBills 应 0 条, got %d", len(list))
	}
}

func TestMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := noTenantCtx()

	// 全部方法：缺失租户即拒（fail-closed）。
	if _, err := s.GetQuota(ctx); err == nil {
		t.Fatalf("GetQuota 缺失租户应拒")
	}
	if _, err := s.SetQuota(ctx, billing.ResourceQuota{}); err == nil {
		t.Fatalf("SetQuota 缺失租户应拒")
	}
	if _, err := s.GetUsage(ctx); err == nil {
		t.Fatalf("GetUsage 缺失租户应拒")
	}
	if _, err := s.IncUsage(ctx, billing.ResWorkloads, 1); err == nil {
		t.Fatalf("IncUsage 缺失租户应拒")
	}
	if _, err := s.CheckAndInc(ctx, billing.ResWorkloads, 1); err == nil {
		t.Fatalf("CheckAndInc 缺失租户应拒")
	}
	if _, err := s.ListBills(ctx); err == nil {
		t.Fatalf("ListBills 缺失租户应拒")
	}
	if _, err := s.GenerateBill(ctx, "2026-07"); err == nil {
		t.Fatalf("GenerateBill 缺失租户应拒")
	}
	if _, err := s.GetBill(ctx, "x"); err == nil {
		t.Fatalf("GetBill 缺失租户应拒")
	}
	if _, err := s.PayBill(ctx, "x"); err == nil {
		t.Fatalf("PayBill 缺失租户应拒")
	}
}

// ---------- Count 方法（seed 判空用） ----------

func TestCountMethods(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 空表。
	if n, _ := s.QuotasCount(ctx); n != 0 {
		t.Fatalf("空表 QuotasCount 应 0, got %d", n)
	}
	if n, _ := s.UsagesCount(ctx); n != 0 {
		t.Fatalf("空表 UsagesCount 应 0, got %d", n)
	}
	if n, _ := s.RecordsCount(ctx); n != 0 {
		t.Fatalf("空表 RecordsCount 应 0, got %d", n)
	}

	// 灌数据。
	s.SetQuota(ctx, billing.ResourceQuota{Limits: map[string]int{}})
	s.IncUsage(ctx, billing.ResApplications, 1)
	s.GenerateBill(ctx, "2026-07")

	if n, _ := s.QuotasCount(ctx); n != 1 {
		t.Fatalf("QuotasCount 应 1, got %d", n)
	}
	if n, _ := s.UsagesCount(ctx); n != 1 {
		t.Fatalf("UsagesCount 应 1, got %d", n)
	}
	if n, _ := s.RecordsCount(ctx); n != 1 {
		t.Fatalf("RecordsCount 应 1, got %d", n)
	}
}

// 编译期断言：pgx.ErrNoRows / errors.Is 用于错误映射（避免误删 import）。
var _ = errors.Is
var _ = pgx.ErrNoRows
var _ = fmt.Sprintf
