package main

import (
	"context"
	"log"
	"os"
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
//
// 生产环境设 PAAS_DISABLE_DEMO_SEED=true 关闭演示凭证（admin/123456 + sk-* 演示 Key），
// 防止开源仓公开的演示值成为生产后门；租户结构与 extraKey（运维配置）不受影响。
func seedIdentity(idb identity.Repository, extraKey string) {
	ctx := context.Background()
	now := time.Now()
	demoDisabled := os.Getenv("PAAS_DISABLE_DEMO_SEED") == "true"

	for _, t := range []identity.Tenant{
		{ID: "t-acme", Name: "Acme", CreatedAt: now},
		{ID: "t-globex", Name: "Globex", CreatedAt: now},
	} {
		if err := idb.CreateTenant(ctx, t); err != nil {
			log.Printf("[seed] %v", err)
		}
	}

	keys := []identity.APIKey{}
	if !demoDisabled {
		// 密码登录用户 admin/123456（幂等：已存在跳过，避免重复 bcrypt）。仅 dev/demo，生产关闭。
		if _, err := idb.GetUserByName(ctx, "admin"); err != nil {
			hash, hErr := auth.HashPassword("123456")
			if hErr != nil {
				log.Printf("[seed] hash admin 密码失败: %v", hErr)
			} else if err := idb.CreateUser(ctx, identity.User{
				ID: "u-super-admin", TenantID: "t-acme", Name: "admin",
				PasswordHash: hash, IsAdmin: true, Roles: []string{"super_admin", "tenant-admin"}, Status: identity.StatusActive,
			}); err != nil {
				log.Printf("[seed] %v", err)
			}
		}
		// 租户密码登录账号（与 3 演示 API Key 的 UserID 对齐，供 console-user 登录）。
		for _, tu := range []struct {
			id, name, tenant, role string
		}{
			{"u-acme-admin", "acme-admin", "t-acme", "tenant-admin"},
			{"u-acme-dev", "acme-dev", "t-acme", "developer"},
			{"u-globex-admin", "globex-admin", "t-globex", "tenant-admin"},
		} {
			if _, err := idb.GetUserByName(ctx, tu.name); err != nil {
				hash, hErr := auth.HashPassword("123456")
				if hErr != nil {
					log.Printf("[seed] hash %s 密码失败: %v", tu.name, hErr)
					continue
				}
				if err := idb.CreateUser(ctx, identity.User{
					ID: tu.id, TenantID: tu.tenant, Name: tu.name,
					PasswordHash: hash, Roles: []string{tu.role}, Status: identity.StatusActive,
				}); err != nil {
					log.Printf("[seed] %v", err)
				}
			}
		}
		keys = append(keys,
			identity.APIKey{ID: "k-acme-admin", TenantID: "t-acme", UserID: "u-acme-admin", Roles: []string{"tenant-admin"}, Key: "sk-acme-admin", CreatedAt: now},
			identity.APIKey{ID: "k-globex-admin", TenantID: "t-globex", UserID: "u-globex-admin", Roles: []string{"tenant-admin"}, Key: "sk-globex-admin", CreatedAt: now},
			identity.APIKey{ID: "k-acme-dev", TenantID: "t-acme", UserID: "u-acme-dev", Roles: []string{"developer"}, Key: "sk-acme-dev", CreatedAt: now},
		)
	} else {
		log.Printf("[seed] PAAS_DISABLE_DEMO_SEED=true，跳过演示凭证（admin/123456 + sk-* Key）")
	}

	// extraKey（来自 PAAS_API_KEY）是运维配置的生产 Key，非演示，始终生效。
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
