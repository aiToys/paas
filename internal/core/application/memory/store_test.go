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

func TestBindResourceIncrements(t *testing.T) {
	s := NewStore()
	a, err := s.BindResource(context.Background(), "app-cs", "mq")
	require.NoError(t, err)
	// seed 中 app-cs 的 mq=1，绑定后应为 2
	assert.Equal(t, 2, a.Resources.MQ)

	_, err = s.BindResource(context.Background(), "app-cs", "ghost")
	assert.Error(t, err, "未知类型应报错")
}
