package memory_test

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/appconfig/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

// TestListSecretMasked 验证 List 时 Secret 值掩码（不泄漏明文）。
func TestListSecretMasked(t *testing.T) {
	s := memory.NewStore()
	ctx := acmeCtx()
	// 自建 secret 配置（去假数据后 store 初始空，测试自建验证掩码）
	if _, err := s.Upsert(ctx, appconfig.ConfigItem{
		AppID: "app-cs", EnvID: "env-acme-test", Key: "API_KEY", Value: "sk-real-secret-value", Type: appconfig.TypeSecret,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx, "app-cs", "env-acme-test")
	var apiKey appconfig.ConfigItem
	for _, c := range list {
		if c.Key == "API_KEY" {
			apiKey = c
		}
	}
	if apiKey.Type != appconfig.TypeSecret {
		t.Fatal("API_KEY 应为 secret 类型")
	}
	if apiKey.Value != appconfig.SecretMask {
		t.Fatalf("secret 值应掩码，got %q", apiKey.Value)
	}
}

// TestUpsertNewAndUpdate 验证新增 + 同 key 更新（幂等）。
func TestUpsertNewAndUpdate(t *testing.T) {
	s := memory.NewStore()
	ctx := acmeCtx()

	c, err := s.Upsert(ctx, appconfig.ConfigItem{
		AppID: "app-cs", EnvID: "env-acme-test", Key: "NEW_KEY", Value: "v1", Type: appconfig.TypeEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("应生成 ID")
	}

	// 同 key 应更新（同 ID），不改明文回显 env 值
	c2, _ := s.Upsert(ctx, appconfig.ConfigItem{
		AppID: "app-cs", EnvID: "env-acme-test", Key: "NEW_KEY", Value: "v2", Type: appconfig.TypeEnv,
	})
	if c2.ID != c.ID {
		t.Fatalf("同 key 应更新同 ID，got %s vs %s", c2.ID, c.ID)
	}
	if c2.Value != "v2" {
		t.Fatalf("env 更新值应为 v2，got %s", c2.Value)
	}
}

// TestUpsertSecretMaskedReturn 验证 Upsert 返回的 Secret 已掩码。
func TestUpsertSecretMaskedReturn(t *testing.T) {
	s := memory.NewStore()
	c, _ := s.Upsert(acmeCtx(), appconfig.ConfigItem{
		AppID: "app-cs", EnvID: "env-acme-test", Key: "SEC", Value: "plaintext", Type: appconfig.TypeSecret,
	})
	if c.Value != appconfig.SecretMask {
		t.Fatalf("Upsert 返回的 secret 应掩码，got %q", c.Value)
	}
}

// TestTenantIsolation 验证跨租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := memory.NewStore()
	gctx := tenant.WithTenant(context.Background(), "t-globex")

	list, _ := s.List(gctx, "", "")
	for _, c := range list {
		if c.TenantID != "t-globex" {
			t.Fatal("不应见到其他租户配置")
		}
	}
	// acme 删 globex 配置 -> 失败
	if err := s.Delete(acmeCtx(), "cfg-globex-1"); err == nil {
		t.Fatal("跨租户删除应失败")
	}
}

func TestDelete(t *testing.T) {
	s := memory.NewStore()
	ctx := acmeCtx()
	c, _ := s.Upsert(ctx, appconfig.ConfigItem{
		AppID: "app-cs", EnvID: "env-acme-test", Key: "TO_DEL", Value: "x", Type: appconfig.TypeEnv,
	})
	if err := s.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx, "app-cs", "env-acme-test")
	for _, item := range list {
		if item.ID == c.ID {
			t.Fatal("应已删除")
		}
	}
}

func TestValidate(t *testing.T) {
	s := memory.NewStore()
	ctx := acmeCtx()
	if _, err := s.Upsert(ctx, appconfig.ConfigItem{Key: "", Value: "x", Type: appconfig.TypeEnv}); err == nil {
		t.Fatal("空 key 应校验失败")
	}
	if _, err := s.Upsert(ctx, appconfig.ConfigItem{Key: "K", Value: "x", Type: "bogus"}); err == nil {
		t.Fatal("非法 type 应校验失败")
	}
}
