package main

import (
	"context"
	"log"
	"time"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/identity"
)

// seedIdentity 注入演示用租户与 API Key。
//
//	两租户：Acme(t-acme) / Globex(t-globex)
//	三 Key：sk-acme-admin / sk-globex-admin / sk-acme-dev（developer，验证权限差异）
//	一密码登录用户：admin/123456（t-acme 管理员，供 console-admin 登录）
//
// extraKey（来自 PAAS_API_KEY）若非内置 Key，追加为 t-acme 的 admin Key，兼容自定义部署。
func seedIdentity(idb identity.Repository, extraKey string) {
	ctx := context.Background()
	now := time.Now()

	for _, t := range []identity.Tenant{
		{ID: "t-acme", Name: "Acme", CreatedAt: now},
		{ID: "t-globex", Name: "Globex", CreatedAt: now},
	} {
		if err := idb.CreateTenant(ctx, t); err != nil {
			log.Printf("[seed] %v", err)
		}
	}

	// 密码登录用户 admin/123456（幂等：已存在跳过，避免重复 bcrypt）。
	if _, err := idb.GetUserByName(ctx, "admin"); err != nil {
		hash, hErr := auth.HashPassword("123456")
		if hErr != nil {
			log.Printf("[seed] hash admin 密码失败: %v", hErr)
		} else if err := idb.CreateUser(ctx, identity.User{
			ID: "u-acme-admin", TenantID: "t-acme", Name: "admin",
			PasswordHash: hash, IsAdmin: true, Roles: []string{"tenant-admin"}, Status: identity.StatusActive,
		}); err != nil {
			log.Printf("[seed] %v", err)
		}
	}

	keys := []identity.APIKey{
		{ID: "k-acme-admin", TenantID: "t-acme", UserID: "u-acme-admin", Roles: []string{"tenant-admin"}, Key: "sk-acme-admin", CreatedAt: now},
		{ID: "k-globex-admin", TenantID: "t-globex", UserID: "u-globex-admin", Roles: []string{"tenant-admin"}, Key: "sk-globex-admin", CreatedAt: now},
		{ID: "k-acme-dev", TenantID: "t-acme", UserID: "u-acme-dev", Roles: []string{"developer"}, Key: "sk-acme-dev", CreatedAt: now},
	}
	if extraKey != "" && extraKey != "sk-acme-admin" && extraKey != "sk-globex-admin" && extraKey != "sk-acme-dev" {
		keys = append(keys, identity.APIKey{
			ID: "k-extra", TenantID: "t-acme", UserID: "u-extra", Roles: []string{"tenant-admin"}, Key: extraKey, CreatedAt: now,
		})
	}
	for _, k := range keys {
		if err := idb.CreateAPIKey(ctx, k); err != nil {
			log.Printf("[seed] %v", err)
		}
	}
}
