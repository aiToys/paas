package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/pkg/tenant"
)

// acme 上下文：seed 中 app-cs/rec/lab 属 t-acme。
func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func TestSeedAndList(t *testing.T) {
	s := NewStore()
	apps, err := s.List(acmeCtx())
	require.NoError(t, err)
	assert.Greater(t, len(apps), 0, "seed 后应有应用")
	for _, a := range apps {
		assert.Equal(t, "t-acme", a.TenantID, "列表不应跨租户泄漏")
	}
	// 列表应按 ID 稳定排序
	for i := 1; i < len(apps); i++ {
		assert.Less(t, apps[i-1].ID, apps[i].ID, "列表应按 ID 升序")
	}
}

func TestCreateAndGet(t *testing.T) {
	s := NewStore()
	err := s.Create(acmeCtx(), application.Application{
		ID: "app-x", Name: "测试应用", Env: "开发", Status: "idle",
	})
	require.NoError(t, err)

	got, err := s.Get(acmeCtx(), "app-x")
	require.NoError(t, err)
	assert.Equal(t, "测试应用", got.Name)
	assert.Equal(t, "t-acme", got.TenantID, "Create 应从 ctx 写入租户")
}

func TestBindResourceIncrementsAndIdempotent(t *testing.T) {
	s := NewStore()
	before, err := s.Get(acmeCtx(), "app-cs")
	require.NoError(t, err)
	require.Equal(t, 0, before.Resources.MQ, "seed 移除 mock 绑定，初始应为 0")

	a, err := s.BindResource(acmeCtx(), "app-cs", "mq", "mq-new")
	require.NoError(t, err)
	assert.Equal(t, 1, a.Resources.MQ, "绑定后 mq 计数应 +1")
	assert.Len(t, a.Bindings, len(before.Bindings)+1, "绑定项应追加")

	a2, err := s.BindResource(acmeCtx(), "app-cs", "mq", "mq-new")
	require.NoError(t, err)
	assert.Equal(t, 1, a2.Resources.MQ, "重复绑定应幂等")
}

func TestBindResourceValidation(t *testing.T) {
	s := NewStore()
	_, err := s.BindResource(acmeCtx(), "app-cs", "ghost", "x")
	assert.Error(t, err, "未知类型应报错")

	_, err = s.BindResource(acmeCtx(), "app-cs", "mq", "")
	assert.Error(t, err, "空名称应报错")

	_, err = s.BindResource(acmeCtx(), "ghost", "mq", "x")
	assert.Error(t, err, "不存在应用应报错")
}

func TestUnbind(t *testing.T) {
	s := NewStore()
	// 先绑定再解绑（seed 移除 mock 绑定，测试自建绑定验证解绑语义）
	_, err := s.BindResource(acmeCtx(), "app-cs", "mq", "mq-order-events")
	require.NoError(t, err)

	a, err := s.Unbind(acmeCtx(), "app-cs", "mq", "mq-order-events")
	require.NoError(t, err)
	assert.Equal(t, 0, a.Resources.MQ, "解绑后 mq 计数应归零")

	_, err = s.Unbind(acmeCtx(), "app-cs", "mq", "mq-order-events")
	assert.Error(t, err, "重复解绑应报错")
}

// —— 多租户隔离 ——

func TestListIsolatedByTenant(t *testing.T) {
	s := NewStore()
	acme, err := s.List(tenant.WithTenant(context.Background(), "t-acme"))
	require.NoError(t, err)
	globex, err := s.List(tenant.WithTenant(context.Background(), "t-globex"))
	require.NoError(t, err)
	for _, a := range acme {
		assert.Equal(t, "t-acme", a.TenantID)
	}
	for _, a := range globex {
		assert.Equal(t, "t-globex", a.TenantID)
	}
	// app-etl 属 globex，不应出现在 acme 列表
	for _, a := range acme {
		assert.NotEqual(t, "app-etl", a.ID, "acme 列表不应含 globex 应用")
	}
}

func TestGetRejectsCrossTenant(t *testing.T) {
	s := NewStore()
	// app-etl 属 globex，acme 访问应 not found（不泄漏存在性）
	_, err := s.Get(tenant.WithTenant(context.Background(), "t-acme"), "app-etl")
	assert.Error(t, err)
	_, err = s.Get(tenant.WithTenant(context.Background(), "t-globex"), "app-etl")
	require.NoError(t, err)
}

func TestMissingTenantRejected(t *testing.T) {
	s := NewStore()
	_, err := s.List(context.Background())
	assert.Error(t, err, "缺租户上下文应拒绝")
	_, err = s.Get(context.Background(), "app-cs")
	assert.Error(t, err)
}

func TestCreateIgnoresBodyTenant(t *testing.T) {
	s := NewStore()
	// 请求体伪造 globex 租户；ctx 是 acme，应以 ctx 为准
	err := s.Create(acmeCtx(), application.Application{ID: "hack", TenantID: "t-globex"})
	require.NoError(t, err)
	got, err := s.Get(acmeCtx(), "hack")
	require.NoError(t, err)
	assert.Equal(t, "t-acme", got.TenantID, "TenantID 必须取自 ctx，忽略请求体")
}

func TestNewStore_DemoDisabled(t *testing.T) {
	// PAAS_DISABLE_DEMO_SEED=true 时 NewStore 不灌演示应用，生产由用户自建。
	t.Setenv("PAAS_DISABLE_DEMO_SEED", "true")
	s := NewStore()
	apps, err := s.List(acmeCtx())
	require.NoError(t, err)
	assert.Empty(t, apps, "demo 禁用时不应 seed 应用")
}

func TestNewStore_DemoEnabled(t *testing.T) {
	// 默认（dev）灌入示例应用，acme 应有 app-cs/app-rec/app-lab 三个。
	s := NewStore()
	apps, err := s.List(acmeCtx())
	require.NoError(t, err)
	assert.Len(t, apps, 3, "acme 应有 3 个 seed 应用")
}
