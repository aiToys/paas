package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/plugin"
	"github.com/aitoys/paas/pkg/provider"
)

// capturePlugin 是测试用插件，记录 Init/Run 是否被调用。
type capturePlugin struct {
	name        string
	inited, ran bool
}

func (c *capturePlugin) Manifest() plugin.Manifest   { return plugin.Manifest{Name: c.name} }
func (c *capturePlugin) Routes() []plugin.RouteSpec  { return nil }
func (c *capturePlugin) Schemas() []plugin.CRDSchema { return nil }
func (c *capturePlugin) Meters() []plugin.MeterSpec  { return nil }
func (c *capturePlugin) Init(context.Context, plugin.CoreDeps) error {
	c.inited = true
	return nil
}
func (c *capturePlugin) Run(context.Context) error {
	c.ran = true
	return nil
}

// testCoreDeps 提供 Gateway() 返回 nil 的最小 CoreDeps（capturePlugin 不消费它）。
type testCoreDeps struct{}

func (testCoreDeps) Logger() interface{}                { return nil }
func (testCoreDeps) Gateway() provider.GatewayRegistrar { return nil }
func (testCoreDeps) SecretResolver() provider.CredentialResolver {
	return nil
}

func TestBootstrapInitializesAndRunsPlugins(t *testing.T) {
	p := &capturePlugin{name: "maas"}
	ran, err := bootstrapCore(context.Background(), []plugin.Plugin{p}, testCoreDeps{})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"maas": true}, ran)
	assert.True(t, p.inited)
	assert.True(t, p.ran)
}

func TestResolveAPIKeyDefaultsAndOverride(t *testing.T) {
	t.Setenv("PAAS_API_KEY", "")
	assert.Equal(t, "sk-acme-admin", resolveAPIKey())

	t.Setenv("PAAS_API_KEY", "sk-custom")
	assert.Equal(t, "sk-custom", resolveAPIKey())
}

func TestResolveJWTSecret_ProductionRejectsEmpty(t *testing.T) {
	t.Setenv("PAAS_JWT_SECRET", "")
	t.Setenv("PAAS_PROD", "true")

	_, err := resolveJWTSecretOrErr()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PAAS_JWT_SECRET")
}

func TestResolveJWTSecret_DevAllowsRandom(t *testing.T) {
	t.Setenv("PAAS_JWT_SECRET", "")
	t.Setenv("PAAS_PROD", "")

	s, err := resolveJWTSecretOrErr()
	require.NoError(t, err)
	assert.NotEmpty(t, s)
}

func TestResolveJWTSecret_UsesEnvIfSet(t *testing.T) {
	// 生产模式显式 secret 需 ≥32 字节（防弱串暴破）。
	t.Setenv("PAAS_JWT_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("PAAS_PROD", "true") // 生产模式但有显式 secret，应通过

	s, err := resolveJWTSecretOrErr()
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef", s)
}

func TestResolveJWTSecret_ProductionRejectsShort(t *testing.T) {
	// 生产模式 secret <32 字节拒启（防弱串暴破）。
	t.Setenv("PAAS_JWT_SECRET", "my-explicit-secret")
	t.Setenv("PAAS_PROD", "true")

	_, err := resolveJWTSecretOrErr()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "过短")
}
