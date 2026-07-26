package maas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/provider"
)

type fakeRegistrar struct {
	registered map[string]provider.Provider
}

func (f *fakeRegistrar) Register(model string, p provider.Provider) {
	f.registered[model] = p
}

type stubDeps struct{ gw provider.GatewayRegistrar }

func (stubDeps) Logger() interface{}                  { return nil }
func (s stubDeps) Gateway() provider.GatewayRegistrar { return s.gw }

func TestMaaSPluginRegistersEchoProvider(t *testing.T) {
	reg := &fakeRegistrar{registered: map[string]provider.Provider{}}
	p := &MaaSPlugin{}

	require.NoError(t, p.Init(context.Background(), stubDeps{gw: reg}))
	require.NoError(t, p.Run(context.Background()))

	got, ok := reg.registered["echo"]
	require.True(t, ok, "应注册 echo 模型路由")
	assert.Equal(t, "echo", got.Name())
	assert.Equal(t, "maas", p.Manifest().Name)
}

func TestMaaSPluginInitFailsWithoutGateway(t *testing.T) {
	p := &MaaSPlugin{}
	err := p.Init(context.Background(), stubDeps{gw: nil})
	require.Error(t, err)
}
