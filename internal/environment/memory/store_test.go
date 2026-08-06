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

// TestSeedPromoteOrder 验证 seed 环境带默认阶序（test=10/prod=20，参与发布流水线）。
func TestSeedPromoteOrder(t *testing.T) {
	s := NewStore()
	list, _ := s.List(acmeCtx())
	for _, e := range list {
		switch e.Type {
		case environment.TypeTest:
			assert.Equal(t, 10, e.PromoteOrder, "test 环境阶序应 10: %s", e.ID)
		case environment.TypeProd:
			assert.Equal(t, 20, e.PromoteOrder, "prod 环境阶序应 20: %s", e.ID)
		}
	}
}

// TestNextPromoteTarget 验证逐级提升：test → prod；prod 已最高阶返 ErrNoPromoteTarget；跨租户不泄漏。
func TestNextPromoteTarget(t *testing.T) {
	s := NewStore()
	// acme test(order=10) → 下个阶序最小 prod（prod-bj/prod-sh 均 20，取 id 最小 = prod-bj）
	next, err := s.NextPromoteTarget(acmeCtx(), "env-acme-test")
	require.NoError(t, err)
	assert.Equal(t, environment.TypeProd, next.Type)
	assert.Equal(t, "env-acme-prod-bj", next.ID, "同 order 取 id 最小")
	// acme prod-bj(order=20) 已最高阶 → ErrNoPromoteTarget
	_, err = s.NextPromoteTarget(acmeCtx(), "env-acme-prod-bj")
	assert.ErrorIs(t, err, environment.ErrNoPromoteTarget)
	// 跨租户：globex 查 acme 环境 → ErrNoPromoteTarget（不泄漏 acme 环境存在性）
	_, err = s.NextPromoteTarget(globexCtx(), "env-acme-test")
	assert.ErrorIs(t, err, environment.ErrNoPromoteTarget)
	// globex test → globex prod（不跨租户）
	next, err = s.NextPromoteTarget(globexCtx(), "env-globex-test")
	require.NoError(t, err)
	assert.Equal(t, "env-globex-prod", next.ID)
}
