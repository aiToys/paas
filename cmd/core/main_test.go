package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aitoys/paas/pkg/plugin"
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

// TestBootstrapInitializesAndRunsPlugins 验证 bootstrapCore 按拓扑顺序 Init+Run 所有插件。
func TestBootstrapInitializesAndRunsPlugins(t *testing.T) {
	p := &capturePlugin{name: "maas"}
	ran, err := bootstrapCore(context.Background(), []plugin.Plugin{p})
	assert.NoError(t, err)
	assert.Equal(t, map[string]bool{"maas": true}, ran)
	assert.True(t, p.inited)
	assert.True(t, p.ran)
}
