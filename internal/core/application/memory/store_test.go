package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/application"
)

func TestSeedAndList(t *testing.T) {
	s := NewStore()
	apps, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Greater(t, len(apps), 0, "seed 后应有应用")
	// 列表应按 ID 稳定排序
	for i := 1; i < len(apps); i++ {
		assert.Less(t, apps[i-1].ID, apps[i].ID, "列表应按 ID 升序")
	}
}

func TestCreateAndGet(t *testing.T) {
	s := NewStore()
	err := s.Create(context.Background(), application.Application{
		ID: "app-x", Name: "测试应用", Env: "开发", Status: "idle",
	})
	require.NoError(t, err)

	got, err := s.Get(context.Background(), "app-x")
	require.NoError(t, err)
	assert.Equal(t, "测试应用", got.Name)
}

func TestBindResourceIncrementsAndIdempotent(t *testing.T) {
	s := NewStore()
	// seed 中 app-cs 的 mq=1
	before, err := s.Get(context.Background(), "app-cs")
	require.NoError(t, err)
	require.Equal(t, 1, before.Resources.MQ)

	a, err := s.BindResource(context.Background(), "app-cs", "mq", "mq-new")
	require.NoError(t, err)
	assert.Equal(t, 2, a.Resources.MQ, "绑定后 mq 计数应 +1")
	assert.Len(t, a.Bindings, len(before.Bindings)+1, "绑定项应追加")

	// 幂等：同名同类型再次绑定不应重复
	a2, err := s.BindResource(context.Background(), "app-cs", "mq", "mq-new")
	require.NoError(t, err)
	assert.Equal(t, 2, a2.Resources.MQ, "重复绑定应幂等")
}

func TestBindResourceValidation(t *testing.T) {
	s := NewStore()
	_, err := s.BindResource(context.Background(), "app-cs", "ghost", "x")
	assert.Error(t, err, "未知类型应报错")

	_, err = s.BindResource(context.Background(), "app-cs", "mq", "")
	assert.Error(t, err, "空名称应报错")

	_, err = s.BindResource(context.Background(), "ghost", "mq", "x")
	assert.Error(t, err, "不存在应用应报错")
}

func TestUnbind(t *testing.T) {
	s := NewStore()
	a, err := s.Unbind(context.Background(), "app-cs", "mq", "mq-order-events")
	require.NoError(t, err)
	assert.Equal(t, 0, a.Resources.MQ, "解绑后 mq 计数应归零")

	// 解绑不存在的项应报错
	_, err = s.Unbind(context.Background(), "app-cs", "mq", "mq-order-events")
	assert.Error(t, err, "重复解绑应报错")
}
