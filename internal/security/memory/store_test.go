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
	if platform != 3 {
		t.Fatalf("平台级凭证应 3 个（供应商 Key），got %d", platform)
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
