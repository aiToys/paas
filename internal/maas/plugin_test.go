package maas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/provider"
)

type fakeRegistrar struct {
	registered map[string]*provider.Model
}

func (f *fakeRegistrar) RegisterModel(m *provider.Model) error {
	f.registered[m.ID] = m
	return nil
}

type stubDeps struct{ gw provider.GatewayRegistrar }

func (stubDeps) Logger() interface{}                  { return nil }
func (s stubDeps) Gateway() provider.GatewayRegistrar { return s.gw }

func TestMaaSPluginRegistersCatalog(t *testing.T) {
	reg := &fakeRegistrar{registered: map[string]*provider.Model{}}
	p := &MaaSPlugin{}

	require.NoError(t, p.Init(context.Background(), stubDeps{gw: reg}))
	require.NoError(t, p.Run(context.Background()))

	assert.Equal(t, "maas", p.Manifest().Name)
	require.Len(t, reg.registered, len(catalog()), "应注册 catalog 全部模型")

	// 抽查：qwen2.5-7b 含主备两通道，且通道均已绑定 Provider
	m := reg.registered["qwen2.5-7b"]
	require.NotNil(t, m)
	require.Len(t, m.Channels, 2, "qwen2.5-7b 应挂主备两通道")
	for _, c := range m.Channels {
		assert.NotNil(t, c.Impl(), "通道 %s 必须绑定 Provider", c.ID)
	}
}

func TestMaaSPluginInitFailsWithoutGateway(t *testing.T) {
	p := &MaaSPlugin{}
	err := p.Init(context.Background(), stubDeps{gw: nil})
	require.Error(t, err)
}
