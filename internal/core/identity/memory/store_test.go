package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/identity"
)

func TestCreateAndGetTenant(t *testing.T) {
	s := NewStore()
	tnt := identity.Tenant{ID: "t1", Name: "acme", CreatedAt: time.Now()}
	require.NoError(t, s.CreateTenant(context.Background(), tnt))

	got, err := s.GetTenant(context.Background(), "t1")
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Name)
}

func TestUsersByTenantIsolation(t *testing.T) {
	s := NewStore()
	_ = s.CreateTenant(context.Background(), identity.Tenant{ID: "t1", Name: "a", CreatedAt: time.Now()})
	_ = s.CreateTenant(context.Background(), identity.Tenant{ID: "t2", Name: "b", CreatedAt: time.Now()})
	require.NoError(t, s.CreateUser(context.Background(), identity.User{ID: "u1", TenantID: "t1", Name: "alice"}))
	require.NoError(t, s.CreateUser(context.Background(), identity.User{ID: "u2", TenantID: "t2", Name: "bob"}))

	users, err := s.UsersByTenant(context.Background(), "t1")
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "alice", users[0].Name) // 不应泄漏 t2 的 bob
}

func TestLookupAPIKey(t *testing.T) {
	s := NewStore()
	require.NoError(t, s.CreateTenant(context.Background(), identity.Tenant{ID: "t-acme", Name: "Acme", CreatedAt: time.Now()}))
	require.NoError(t, s.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k1", TenantID: "t-acme", UserID: "u1", Roles: []string{"developer"}, Key: "sk-acme-dev",
	}))

	got, err := s.LookupAPIKey(context.Background(), "sk-acme-dev")
	require.NoError(t, err)
	assert.Equal(t, "t-acme", got.TenantID)
	assert.Equal(t, []string{"developer"}, got.Roles)
}

func TestLookupAPIKeyUnknown(t *testing.T) {
	_, err := NewStore().LookupAPIKey(context.Background(), "sk-nope")
	assert.Error(t, err)
}

func TestBuiltinRoleAdminPassAll(t *testing.T) {
	r := identity.BuiltinRoles()["tenant-admin"]
	assert.Contains(t, r.Permissions, identity.Permission("tenant:admin"))
	assert.True(t, r.Grants("application:write"))
	assert.True(t, r.Grants("model:infer"))
}

func TestBuiltinRoleDeveloperScoped(t *testing.T) {
	r := identity.BuiltinRoles()["developer"]
	assert.True(t, r.Grants("model:infer"))
	assert.True(t, r.Grants("application:write"))
	assert.False(t, r.Grants("tenant:admin"))
}

func TestBuiltinRoleViewerReadonly(t *testing.T) {
	r := identity.BuiltinRoles()["viewer"]
	assert.True(t, r.Grants("application:read"))
	assert.False(t, r.Grants("application:write"))
	assert.False(t, r.Grants("model:infer"))
}

func TestBuiltinRoleDeveloperNoProdWrite(t *testing.T) {
	r := identity.BuiltinRoles()["developer"]
	// developer 测试环境可写
	assert.True(t, r.Grants("workload:write"))
	assert.True(t, r.Grants("environment:write"))
	// developer 生产环境只读：无 prod:write
	assert.False(t, r.Grants("prod:write"))
}

func TestBuiltinRoleAdminHasProdWrite(t *testing.T) {
	r := identity.BuiltinRoles()["tenant-admin"]
	assert.True(t, r.Grants("prod:write"), "admin 应有生产写权限")
}

// TestDeleteUserRevokesAPIKeys 验证删除用户级联吊销其 API Key：
// LookupAPIKey 不校验用户存在性，不级联则已删用户的凭证永久有效（安全回归防护）。
func TestDeleteUserRevokesAPIKeys(t *testing.T) {
	s := NewStore()
	require.NoError(t, s.CreateTenant(context.Background(), identity.Tenant{ID: "t1", Name: "a", CreatedAt: time.Now()}))
	require.NoError(t, s.CreateUser(context.Background(), identity.User{ID: "u1", TenantID: "t1", Name: "alice"}))
	require.NoError(t, s.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k1", TenantID: "t1", UserID: "u1", Roles: []string{"developer"}, Key: "sk-alice",
	}))
	// 他人 Key 不应被误删。
	require.NoError(t, s.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k2", TenantID: "t1", UserID: "u2", Roles: []string{"viewer"}, Key: "sk-bob",
	}))

	require.NoError(t, s.DeleteUser(context.Background(), "t1", "u1"))

	_, err := s.LookupAPIKey(context.Background(), "sk-alice")
	assert.Error(t, err, "删除用户后其 API Key 必须失效（凭证随身份消亡）")
	got, err := s.LookupAPIKey(context.Background(), "sk-bob")
	require.NoError(t, err)
	assert.Equal(t, "u2", got.UserID, "不应误删其他用户的 Key")
}

func TestGetUserByNameAndIsolation(t *testing.T) {
	s := NewStore()
	require.NoError(t, s.CreateUser(context.Background(), identity.User{
		ID: "u1", TenantID: "t1", Name: "alice", Status: identity.StatusActive,
		Email: "a@x.com", PasswordHash: "h",
	}))

	got, err := s.GetUserByName(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "u1", got.ID)
	assert.Equal(t, "a@x.com", got.Email)
	assert.Equal(t, "h", got.PasswordHash)
	assert.Equal(t, identity.StatusActive, got.Status)

	_, err = s.GetUserByName(context.Background(), "nobody")
	assert.Error(t, err)

	// GetUser 租户内隔离：正确租户命中，跨租户不泄漏
	got2, err := s.GetUser(context.Background(), "t1", "u1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got2.Name)
	_, err = s.GetUser(context.Background(), "t2", "u1")
	assert.Error(t, err)
}

// —— 生命周期（D5）——

// 冻结租户的 Key 一律拒绝；恢复后立即可用（控制面准入，非数据面停机）。
func TestTenantFreezeBlocksAPIKey(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.CreateTenant(ctx, identity.Tenant{ID: "t1", Name: "T1"}))
	require.NoError(t, s.CreateAPIKey(ctx, identity.APIKey{
		ID: "k1", TenantID: "t1", UserID: "u1", Key: "sk-1", Roles: []string{"developer"},
	}))
	require.NoError(t, func() error { _, err := s.LookupAPIKey(ctx, "sk-1"); return err }())

	frozen, err := s.SetTenantStatus(ctx, "t1", identity.TenantStatusFrozen, "欠费")
	require.NoError(t, err)
	assert.True(t, frozen.IsFrozen())
	require.NotNil(t, frozen.FrozenAt)
	assert.Equal(t, "欠费", frozen.FrozenReason)

	_, err = s.LookupAPIKey(ctx, "sk-1")
	require.ErrorIs(t, err, identity.ErrTenantFrozen)

	active, err := s.SetTenantStatus(ctx, "t1", identity.TenantStatusActive, "")
	require.NoError(t, err)
	assert.Nil(t, active.FrozenAt)
	assert.Empty(t, active.FrozenReason)
	_, err = s.LookupAPIKey(ctx, "sk-1")
	require.NoError(t, err)
}

// 到期与吊销分别返回独立哨兵：调用方需能区分「过期」与「吊销」。
func TestAPIKeyExpiryAndRevocation(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.CreateTenant(ctx, identity.Tenant{ID: "t1", Name: "T1"}))
	require.NoError(t, s.CreateAPIKey(ctx, identity.APIKey{ID: "k1", TenantID: "t1", UserID: "u1", Key: "sk-1"}))

	past := time.Now().Add(-time.Minute)
	require.NoError(t, s.SetAPIKeyExpiry(ctx, "k1", past))
	_, err := s.LookupAPIKey(ctx, "sk-1")
	require.ErrorIs(t, err, identity.ErrAPIKeyExpired)

	// 取消到期（零值 = 永不过期）
	require.NoError(t, s.SetAPIKeyExpiry(ctx, "k1", time.Time{}))
	_, err = s.LookupAPIKey(ctx, "sk-1")
	require.NoError(t, err)

	require.NoError(t, s.RevokeAPIKey(ctx, "k1", time.Now()))
	_, err = s.LookupAPIKey(ctx, "sk-1")
	require.ErrorIs(t, err, identity.ErrAPIKeyRevoked)

	// 吊销是软删：记录仍在，供管理页与审计核对
	k, err := s.GetAPIKey(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, "t1", k.TenantID)
	assert.Equal(t, "revoked", k.KeyState(time.Now()))
}

func TestTouchAPIKeyUsage(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.CreateTenant(ctx, identity.Tenant{ID: "t1", Name: "T1"}))
	require.NoError(t, s.CreateAPIKey(ctx, identity.APIKey{ID: "k1", TenantID: "t1", UserID: "u1", Key: "sk-1"}))

	at := time.Now().Truncate(time.Second)
	require.NoError(t, s.TouchAPIKeyUsage(ctx, "k1", at))
	k, err := s.GetAPIKey(ctx, "k1")
	require.NoError(t, err)
	require.NotNil(t, k.LastUsedAt)
	assert.True(t, at.Equal(*k.LastUsedAt))

	// Key 已删除：回写不视为错误（使用统计失败不能升级为鉴权失败）
	require.NoError(t, s.TouchAPIKeyUsage(ctx, "missing", at))
}

// 会话代次：撤销 +1；跨租户定位需显式传租户；普通更新不得把代次写回 0。
func TestRevokeUserSessions(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.CreateTenant(ctx, identity.Tenant{ID: "t1", Name: "T1"}))
	require.NoError(t, s.CreateUser(ctx, identity.User{ID: "u1", TenantID: "t1", Name: "alice"}))

	v, err := s.RevokeUserSessions(ctx, "t1", "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, v)

	// 租户不符 / 用户不存在：均报错（不泄漏存在性由 handler 层负责）。
	_, err = s.RevokeUserSessions(ctx, "t2", "u1")
	require.Error(t, err)

	// 普通资料更新保留代次（只有 RevokeUserSessions 能改）。
	require.NoError(t, s.UpdateUser(ctx, identity.User{ID: "u1", TenantID: "t1", Name: "alice2"}))
	u, err := s.GetUser(ctx, "t1", "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, u.SessionVersion)

	// 空租户 = 跨租户按用户 ID 定位（平台超管路径）。
	v, err = s.RevokeUserSessions(ctx, "", "u1")
	require.NoError(t, err)
	assert.Equal(t, 2, v)
}
