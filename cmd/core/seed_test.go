package main

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
)

func TestSeedIdentity_TenantPasswordAccounts(t *testing.T) {
	idb := idmemory.NewStore()
	seedIdentity(idb, "")

	cases := []struct{ name, tenant, wantRole string }{
		{"acme-admin", "t-acme", "tenant-admin"},
		{"acme-dev", "t-acme", "developer"},
		{"globex-admin", "t-globex", "tenant-admin"},
	}
	for _, c := range cases {
		u, err := idb.GetUserByName(context.Background(), c.name)
		if err != nil {
			t.Fatalf("seed 未建用户 %s: %v", c.name, err)
		}
		if u.TenantID != c.tenant {
			t.Errorf("%s 租户错: got %s want %s", c.name, u.TenantID, c.tenant)
		}
		// 校验密码可验过（seed 用 123456）
		if !auth.CheckPassword(u.PasswordHash, "123456") {
			t.Errorf("%s 密码哈希不可验", c.name)
		}
		// 角色含期望
		if !containsRole(u.Roles, c.wantRole) {
			t.Errorf("%s 角色缺 %s: %v", c.name, c.wantRole, u.Roles)
		}
	}
	_ = identity.StatusActive // 引用包
}

func containsRole(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
