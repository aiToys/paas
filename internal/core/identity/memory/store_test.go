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
