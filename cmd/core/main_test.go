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
