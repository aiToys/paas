package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/security"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestSecretMasked 验证 Secret 返回掩码。
func TestSecretMasked(t *testing.T) {
	s := NewStore()
	list, _ := s.ListSecrets(acmeCtx())
	for _, sec := range list {
		if sec.Value != security.SecretMask {
			t.Fatalf("Secret 值应掩码，got %q", sec.Value)
		}
	}
	// Get 同样掩码
	got, _ := s.GetSecret(acmeCtx(), "sec-acme-db")
	if got.Value != security.SecretMask {
		t.Fatalf("GetSecret 值应掩码，got %q", got.Value)
	}
}

// TestTenantIsolation 验证：租户级密钥按租户隔离 + 平台级密钥全租户共享。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListSecrets(acmeCtx())
	globex, _ := s.ListSecrets(globexCtx())
	// 计数租户级（按归属租户）
	acmeTenant, globexTenant, platform := 0, 0, 0
	for _, sec := range acme {
		switch {
		case sec.Scope == security.ScopePlatform:
			platform++
		case sec.TenantID == "t-acme":
			acmeTenant++
		}
	}
	for _, sec := range globex {
		if sec.TenantID == "t-globex" && sec.Scope != security.ScopePlatform {
			globexTenant++
		}
	}
	if acmeTenant != 2 || globexTenant != 1 {
		t.Fatalf("租户级应隔离：acme 2、globex 1，got %d/%d", acmeTenant, globexTenant)
	}
	if platform != 4 {
		t.Fatalf("平台级凭证应 4 个（OpenAI/DeepSeek/通义/airouter），got %d", platform)
	}
	// 跨租户 Get 租户级不泄漏
	if _, err := s.GetSecret(acmeCtx(), "sec-globex-token"); err == nil {
		t.Fatal("acme 不应见到 globex 租户级密钥")
	}
	// 平台级全租户可读
	if _, err := s.GetSecret(globexCtx(), "sec-platform-openai"); err != nil {
		t.Fatalf("平台级凭证应全租户可读，got %v", err)
	}
}

// TestResolvePlatformOnly 验证 Resolve 仅返回平台级明文（租户级防绕过掩码）。
func TestResolvePlatformOnly(t *testing.T) {
	s := NewStore()
	// 平台级可解析明文
	sec, err := s.Resolve(acmeCtx(), "sec-platform-openai")
	if err != nil {
		t.Fatalf("Resolve 平台级应成功，got %v", err)
	}
	if sec.Value != "" {
		t.Fatalf("seed 平台级凭证值应为空（部署后填写），got %q", sec.Value)
	}
	// 租户级经 Resolve 返回 not found（防绕过掩码读明文）
	if _, err := s.Resolve(acmeCtx(), "sec-acme-db"); err == nil {
		t.Fatal("Resolve 租户级应拒绝（防绕过掩码）")
	}
}

// TestCreateDedup 验证租户内密钥名唯一。
func TestCreateDedup(t *testing.T) {
	s := NewStore()
	_, err := s.CreateSecret(acmeCtx(), security.Secret{Name: "db-password", Type: security.TypeSecret, Value: "x"})
	if err == nil {
		t.Fatal("同名密钥应冲突")
	}
}

// TestAuditRecordAndQuery 验证审计记录 + 查询。
func TestAuditRecordAndQuery(t *testing.T) {
	s := NewStore()
	err := s.RecordAudit(acmeCtx(), security.AuditLog{
		Actor: "u-acme-dev", Action: security.ActionDelete, ResourceType: security.ResourceSecret,
		ResourceID: "sec-acme-db", Detail: "删除测试",
	})
	if err != nil {
		t.Fatalf("记录审计失败: %v", err)
	}
	// 查全部（acme）
	all, _ := s.ListAuditLogs(acmeCtx(), "", "")
	if len(all) != 2 { // seed 1 + 新增 1
		t.Fatalf("acme 审计应 2 条，got %d", len(all))
	}
	// 倒序：最新在前
	if all[0].Action != security.ActionDelete {
		t.Fatalf("最新审计应在前，got action=%s", all[0].Action)
	}
	// 按 action 过滤
	delOnly, _ := s.ListAuditLogs(acmeCtx(), "", security.ActionDelete)
	if len(delOnly) != 1 {
		t.Fatalf("delete 过滤应 1 条，got %d", len(delOnly))
	}
	// 跨租户不可见
	globex, _ := s.ListAuditLogs(globexCtx(), "", "")
	if len(globex) != 1 {
		t.Fatalf("globex 审计应只见自己的，got %d", len(globex))
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.ListSecrets(context.Background()); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

// TestQueryAllAuditLogs 审计后端过滤/分页（D4）：跨租户、时间倒序、Total 为过滤后全量、
// 关键字命中 resourceId/detail、分页切片正确。
func TestQueryAllAuditLogs(t *testing.T) {
	s := NewStore()
	for i := 0; i < 5; i++ {
		if err := s.RecordAudit(acmeCtx(), security.AuditLog{
			Actor: "u1", Action: "create", ResourceType: "workload",
			ResourceID: "wl-" + string(rune('a'+i)), Detail: "第" + string(rune('0'+i)) + "次",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordAudit(globexCtx(), security.AuditLog{
		Actor: "u2", Action: "admin:delete", ResourceType: "secret", ResourceID: "sec-x", Detail: "强制删",
	}); err != nil {
		t.Fatal(err)
	}

	// 跨租户全量：本测试 6 条 + seed 存量，时间倒序（最后写入的最前）。
	p, err := s.QueryAllAuditLogs(context.Background(), security.AuditQuery{})
	if err != nil || p.Total < 6 || len(p.Items) != p.Total {
		t.Fatalf("全量应 ≥6 条且 items==total，got total=%d items=%d err=%v", p.Total, len(p.Items), err)
	}
	if p.Items[0].Action != "admin:delete" {
		t.Fatalf("应时间倒序（最新在前），got %+v", p.Items[0])
	}

	// 租户 + actor 过滤 + 分页：acme/u1 恒 5 条（与 seed 无关），limit=2 offset=2 → 本页 2 条。
	p, _ = s.QueryAllAuditLogs(context.Background(), security.AuditQuery{TenantID: "t-acme", Actor: "u1", Limit: 2, Offset: 2})
	if p.Total != 5 || len(p.Items) != 2 || p.Items[0].TenantID != "t-acme" || p.Items[0].Actor != "u1" {
		t.Fatalf("分页不符: total=%d items=%d", p.Total, len(p.Items))
	}

	// 关键字命中 detail（大小写不敏感由 ILIKE 语义对齐，这里测子串）。
	p, _ = s.QueryAllAuditLogs(context.Background(), security.AuditQuery{Keyword: "第3"})
	if p.Total != 1 {
		t.Fatalf("关键字应命中 1 条，got %d", p.Total)
	}

	// Action 精确匹配（与 Keyword 不同：不模糊）；叠加 actor 隔离 seed。
	p, _ = s.QueryAllAuditLogs(context.Background(), security.AuditQuery{Action: "create", Actor: "u1"})
	if p.Total != 5 {
		t.Fatalf("Action=create+actor=u1 应 5 条，got %d", p.Total)
	}

	// 越界 offset 返回空页但 Total 正确。
	p, _ = s.QueryAllAuditLogs(context.Background(), security.AuditQuery{Actor: "u1", Offset: 100})
	if p.Total != 5 || len(p.Items) != 0 {
		t.Fatalf("越界应空页, got total=%d items=%d", p.Total, len(p.Items))
	}
}
