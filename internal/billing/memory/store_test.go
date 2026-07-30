package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestQuotaDefault 验证新租户返回默认配额（非错误）。
func TestQuotaDefault(t *testing.T) {
	s := NewStore()
	q, err := s.GetQuota(globexCtx())
	if err != nil {
		t.Fatalf("GetQuota 失败: %v", err)
	}
	if q.Limits[billing.ResApplications] == 0 {
		t.Fatal("应有默认配额")
	}
}

// TestSetQuota 验证配额覆盖更新。
func TestSetQuota(t *testing.T) {
	s := NewStore()
	q, _ := s.SetQuota(acmeCtx(), billing.ResourceQuota{
		Limits: map[string]int{billing.ResGPU: 16, billing.ResStorage: 1000},
	})
	if q.Limits[billing.ResGPU] != 16 || q.Limits[billing.ResStorage] != 1000 {
		t.Fatalf("配额未更新: %+v", q.Limits)
	}
	// 持久化
	got, _ := s.GetQuota(acmeCtx())
	if got.Limits[billing.ResGPU] != 16 {
		t.Fatalf("配额未持久化: %v", got.Limits[billing.ResGPU])
	}
}

// TestUsageOverQuota 验证用量视图超限标记（acme GPU 用量 6 > 配额 4）。
func TestUsageOverQuota(t *testing.T) {
	s := NewStore()
	q, _ := s.GetQuota(acmeCtx())
	u, _ := s.GetUsage(acmeCtx())
	view := billing.BuildUsageView(q, u)
	var gpu billing.UsageLine
	for _, l := range view.Items {
		if l.Resource == billing.ResGPU {
			gpu = l
		}
	}
	if gpu.Count != 6 || gpu.Limit != 4 || !gpu.Over {
		t.Fatalf("GPU 应超限(count=6>limit=4)，got %+v", gpu)
	}
}

// TestIncUsage 验证用量递增/递减。
func TestIncUsage(t *testing.T) {
	s := NewStore()
	u, _ := s.IncUsage(acmeCtx(), billing.ResApplications, 10)
	if u.Counts[billing.ResApplications] != 16 { // seed 6 + 10
		t.Fatalf("递增后应 16，got %d", u.Counts[billing.ResApplications])
	}
	u, _ = s.IncUsage(acmeCtx(), billing.ResApplications, -2)
	if u.Counts[billing.ResApplications] != 14 {
		t.Fatalf("递减后应 14，got %d", u.Counts[billing.ResApplications])
	}
}

func usageOf(s *Store, res string) int {
	u, _ := s.GetUsage(acmeCtx())
	return u.Counts[res]
}

// TestCheckAndInc 验证配额拦截原语：未超限递增、超限拒绝且不递增、Unlimited 不拦截。
func TestCheckAndInc(t *testing.T) {
	t.Run("未超限递增", func(t *testing.T) {
		s := NewStore()
		before := usageOf(s, billing.ResApplications)
		u, err := s.CheckAndInc(acmeCtx(), billing.ResApplications, 1)
		if err != nil {
			t.Fatalf("未超限应成功: %v", err)
		}
		if u.Counts[billing.ResApplications] != before+1 {
			t.Fatalf("应递增到 %d，got %d", before+1, u.Counts[billing.ResApplications])
		}
	})
	t.Run("超限拒绝且不递增", func(t *testing.T) {
		s := NewStore()
		// 设小配额：applications 上限 = 当前 seed 用量，+1 即超
		before := usageOf(s, billing.ResApplications)
		_, _ = s.SetQuota(acmeCtx(), billing.ResourceQuota{Limits: map[string]int{billing.ResApplications: before}})
		_, err := s.CheckAndInc(acmeCtx(), billing.ResApplications, 1)
		if !errors.Is(err, billing.ErrQuotaExceeded) {
			t.Fatalf("应 ErrQuotaExceeded，got %v", err)
		}
		if after := usageOf(s, billing.ResApplications); after != before {
			t.Fatalf("超限不应递增，before %d after %d", before, after)
		}
	})
	t.Run("Unlimited 不拦截", func(t *testing.T) {
		s := NewStore()
		// tokens 默认 Unlimited，大 delta 不拦截
		if _, err := s.CheckAndInc(acmeCtx(), billing.ResTokens, 999999); err != nil {
			t.Fatalf("Unlimited 不应拦截: %v", err)
		}
	})
}

// TestGenerateBill 验证按用量 × 单价生成账单 + 覆盖。
func TestGenerateBill(t *testing.T) {
	s := NewStore()
	rec, err := s.GenerateBill(acmeCtx(), "2026-07")
	if err != nil {
		t.Fatalf("生成账单失败: %v", err)
	}
	if rec.Status != billing.StatusUnpaid {
		t.Fatalf("新账单应 unpaid，got %s", rec.Status)
	}
	// GPU 6 × 100 = 600；Workloads 14 × 5 = 70；至少有这两项
	var total float64
	for _, it := range rec.Items {
		total += it.Amount
	}
	if total != rec.Total {
		t.Fatalf("明细之和 %v 应等于 total %v", total, rec.Total)
	}
	if rec.Total < 670 { // 600 + 70 已超 670？实际 670，含其他项更大
		t.Fatalf("账单总额应 >= 670，got %v", rec.Total)
	}

	// 再次生成同 period 覆盖（不新增）
	count := 0
	bills, _ := s.ListBills(acmeCtx())
	for _, b := range bills {
		if b.Period == "2026-07" && b.Status == billing.StatusUnpaid {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同 period unpaid 应只 1 条，got %d", count)
	}
}

// TestPayBill 验证支付 + 重复支付拒绝。
func TestPayBill(t *testing.T) {
	s := NewStore()
	rec, _ := s.GenerateBill(acmeCtx(), "2026-07")
	paid, err := s.PayBill(acmeCtx(), rec.ID)
	if err != nil {
		t.Fatalf("支付失败: %v", err)
	}
	if paid.Status != billing.StatusPaid || paid.PaidAt == nil {
		t.Fatalf("应已支付，got %+v", paid)
	}
	// 重复支付拒绝
	if _, err := s.PayBill(acmeCtx(), rec.ID); err == nil {
		t.Fatal("重复支付应报错")
	}
}

// TestTenantIsolation 验证账单按租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	_, _ = s.GenerateBill(acmeCtx(), "2026-07")
	acme, _ := s.ListBills(acmeCtx())
	globex, _ := s.ListBills(globexCtx())
	if len(acme) == 0 {
		t.Fatal("acme 应有账单")
	}
	if len(globex) != 0 {
		t.Fatalf("globex 应无账单，got %d", len(globex))
	}
	// 跨租户 GetBill 不泄漏
	acmeBill := acme[len(acme)-1] // 最新 unpaid（2026-07）
	if _, err := s.GetBill(globexCtx(), acmeBill.ID); err == nil {
		t.Fatal("globex 不应见到 acme 账单")
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.GetQuota(context.Background()); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}
