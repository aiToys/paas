package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

func TestListIsolatedByTenant(t *testing.T) {
	s := NewStore()
	acme, _ := s.List(acmeCtx())
	globex, _ := s.List(globexCtx())
	require.Len(t, acme, 3)
	require.Len(t, globex, 2)
	for _, e := range acme {
		assert.Equal(t, "t-acme", e.TenantID)
	}
	for _, e := range globex {
		assert.Equal(t, "t-globex", e.TenantID)
	}
}

func TestGetRejectsCrossTenant(t *testing.T) {
	s := NewStore()
	// env-acme-test 属 acme，globex 访问应 not found
	_, err := s.Get(globexCtx(), "env-acme-test")
	assert.Error(t, err)
	got, err := s.Get(acmeCtx(), "env-acme-test")
	require.NoError(t, err)
	assert.Equal(t, "测试", got.Name)
}

func TestCreateValidateAndTenant(t *testing.T) {
	s := NewStore()
	// 缺 name
	err := s.Create(acmeCtx(), environment.Environment{ID: "x", Type: environment.TypeProd})
	assert.Error(t, err)

	// 非法 type
	err = s.Create(acmeCtx(), environment.Environment{ID: "x", Name: "n", Type: "staging"})
	assert.Error(t, err)

	// 合法：TenantID 取自 ctx
	err = s.Create(acmeCtx(), environment.Environment{ID: "env-new", Name: "新环境", Type: environment.TypeTest})
	require.NoError(t, err)
	got, _ := s.Get(acmeCtx(), "env-new")
	assert.Equal(t, "t-acme", got.TenantID)
}

func TestDeleteCrossTenant(t *testing.T) {
	s := NewStore()
	err := s.Delete(globexCtx(), "env-acme-test")
	assert.Error(t, err)
	err = s.Delete(acmeCtx(), "env-acme-test")
	require.NoError(t, err)
	_, err = s.Get(acmeCtx(), "env-acme-test")
	assert.Error(t, err)
}

func TestMissingTenantRejected(t *testing.T) {
	_, err := NewStore().List(context.Background())
	assert.Error(t, err)
}

func TestSeedHasProdAndTest(t *testing.T) {
	s := NewStore()
	list, _ := s.List(acmeCtx())
	types := map[string]bool{}
	for _, e := range list {
		types[e.Type] = true
	}
	assert.True(t, types[environment.TypeProd])
	assert.True(t, types[environment.TypeTest])
}
