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

func (f *fakeRegistrar) UnregisterModel(id string) { delete(f.registered, id) }

type stubDeps struct{ gw provider.GatewayRegistrar }

func (stubDeps) Logger() interface{}                  { return nil }
func (s stubDeps) Gateway() provider.GatewayRegistrar { return s.gw }
func (stubDeps) SecretResolver() provider.CredentialResolver {
	return nil
}

func TestMaaSPluginRegistersCatalog(t *testing.T) {
	reg := &fakeRegistrar{registered: map[string]*provider.Model{}}
	p := &MaaSPlugin{}

	require.NoError(t, p.Init(context.Background(), stubDeps{gw: reg}))
	require.NoError(t, p.Run(context.Background()))

	assert.Equal(t, "maas", p.Manifest().Name)
	require.Len(t, reg.registered, len(catalog(nil)), "应注册 catalog 全部模型")

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

// 从 Repository 加载模型目录（DB 驱动），BuildProvider 重建通道 impl 后注册。
func TestMaaSPluginInitFromStore(t *testing.T) {
	repo := NewMemoryStore()
	require.NoError(t, repo.CreateModel(context.Background(), &provider.Model{ID: "m1", Name: "M1", Vendor: "v"}))
	require.NoError(t, repo.CreateChannel(context.Background(), "m1", &provider.Channel{ID: "m1#echo", Type: ProviderEcho, Status: provider.StatusHealthy}))

	reg := &fakeRegistrar{registered: map[string]*provider.Model{}}
	p := NewMaaSPlugin(repo)
	require.NoError(t, p.Init(context.Background(), stubDeps{gw: reg}))
	require.Len(t, reg.registered, 1)

	m := reg.registered["m1"]
	require.Len(t, m.Channels, 1)
	require.NotNil(t, m.Channels[0].Impl(), "通道 impl 应由 BuildProvider 重建")
	assert.Equal(t, ProviderEcho, m.Channels[0].Impl().Name())
}

// catalog seed 幂等：重复灌入不报错、不翻倍。
func TestSeedCatalogIdempotent(t *testing.T) {
	repo := NewMemoryStore()
	require.NoError(t, SeedCatalog(context.Background(), repo, nil))
	first, _ := repo.ListModels(context.Background())
	require.NotEmpty(t, first, "seed 后目录应非空")

	require.NoError(t, SeedCatalog(context.Background(), repo, nil))
	second, _ := repo.ListModels(context.Background())
	assert.Len(t, second, len(first), "重复 seed 应幂等")
}
