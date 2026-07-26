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
