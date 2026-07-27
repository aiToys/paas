package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/provider"
)

// stubProvider 用于路由测试，按 Name 区分。
type stubProvider struct{ name string }

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.Chunk, error) {
	return nil, nil
}

// 构造一个挂两通道的模型：prio 0 = primary(stub-a)，prio 1 = standby(stub-b)。
func twoChannelModel() *provider.Model {
	m := &provider.Model{ID: "m1", Channels: []*provider.Channel{
		{ID: "m1#a", Priority: 0, Status: provider.StatusHealthy},
		{ID: "m1#b", Priority: 1, Status: provider.StatusHealthy},
	}}
	m.Channels[0].SetImpl(stubProvider{"stub-a"})
	m.Channels[1].SetImpl(stubProvider{"stub-b"})
	return m
}

func TestResolvePicksHighestPriority(t *testing.T) {
	g := New()
	require.NoError(t, g.RegisterModel(twoChannelModel()))

	ch, err := g.Resolve("m1")
	require.NoError(t, err)
	assert.Equal(t, "stub-a", ch.Impl().Name(), "应选 priority 最低的通道")
}

func TestResolveFailsOverOnOffline(t *testing.T) {
	g := New()
	require.NoError(t, g.RegisterModel(twoChannelModel()))

	// 主通道下线，应切换到 standby
	g.MarkChannelStatus("m1", "m1#a", provider.StatusOffline)
	ch, err := g.Resolve("m1")
	require.NoError(t, err)
	assert.Equal(t, "stub-b", ch.Impl().Name(), "主通道 offline 后应切换到 standby")
}

func TestResolveAllOfflineErrors(t *testing.T) {
	g := New()
	require.NoError(t, g.RegisterModel(twoChannelModel()))

	g.MarkChannelStatus("m1", "m1#a", provider.StatusOffline)
	g.MarkChannelStatus("m1", "m1#b", provider.StatusOffline)
	_, err := g.Resolve("m1")
	require.Error(t, err, "全部 offline 应报错")
}

func TestResolveUnknownModelErrors(t *testing.T) {
	g := New()
	_, err := g.Resolve("ghost")
	require.Error(t, err)
}

func TestModelsStableOrder(t *testing.T) {
	g := New()
	require.NoError(t, g.RegisterModel(&provider.Model{ID: "b"}))
	require.NoError(t, g.RegisterModel(&provider.Model{ID: "a"}))

	got := g.Models()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ID)
	assert.Equal(t, "b", got[1].ID, "应按 ID 升序")
}

func TestRegisterModelValidation(t *testing.T) {
	g := New()
	require.Error(t, g.RegisterModel(nil))
	require.Error(t, g.RegisterModel(&provider.Model{ID: ""}))
}
