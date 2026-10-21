package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestListAllUsagesCrossTenant 验证管理路径能跨租户聚合，而租户路径仍强隔离。
func TestListAllUsagesCrossTenant(t *testing.T) {
	s := NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-a")
	ctxB := tenant.WithTenant(context.Background(), "t-b")
	if _, err := s.IncUsage(ctxA, billing.ResTokens, 100); err != nil {
		t.Fatalf("IncUsage A: %v", err)
	}
	if _, err := s.IncUsage(ctxA, billing.ResApplications, 2); err != nil {
		t.Fatalf("IncUsage A: %v", err)
	}
	if _, err := s.IncUsage(ctxB, billing.ResTokens, 7); err != nil {
		t.Fatalf("IncUsage B: %v", err)
	}

	all, err := s.ListAllUsages(context.Background())
	if err != nil {
		t.Fatalf("ListAllUsages: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应聚合两个租户，got %d: %+v", len(all), all)
	}
	if all[0].TenantID != "t-a" || all[1].TenantID != "t-b" {
		t.Fatalf("应按 tenantId 升序: %+v", all)
	}
	if all[0].Counts[billing.ResTokens] != 100 || all[0].Counts[billing.ResApplications] != 2 {
		t.Fatalf("t-a 计数错误: %+v", all[0].Counts)
	}

	// 租户路径仍只看到自己的用量（ListAll 只放宽读取范围，不改变隔离规则）
	ua, err := s.GetUsage(ctxA)
	if err != nil {
		t.Fatalf("GetUsage A: %v", err)
	}
	if ua.Counts[billing.ResTokens] != 100 {
		t.Fatalf("t-a 用量: %+v", ua.Counts)
	}

	// 返回的 map 必须与仓储独立（改动不污染存储）
	all[0].Counts[billing.ResTokens] = 9999
	ua, _ = s.GetUsage(ctxA)
	if ua.Counts[billing.ResTokens] != 100 {
		t.Fatalf("ListAllUsages 返回 map 未深拷贝: %+v", ua.Counts)
	}
}
