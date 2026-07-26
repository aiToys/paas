package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubPlugin 用于验证契约可被实现并返回 Manifest。
type stubPlugin struct{ name, version string }

func (s *stubPlugin) Manifest() Manifest {
	return Manifest{Name: s.name, Version: s.version}
}
func (s *stubPlugin) Routes() []RouteSpec                  { return nil }
func (s *stubPlugin) Schemas() []CRDSchema                 { return nil }
func (s *stubPlugin) Meters() []MeterSpec                  { return nil }
func (s *stubPlugin) Init(context.Context, CoreDeps) error { return nil }
func (s *stubPlugin) Run(context.Context) error            { return nil }

func TestPluginManifest(t *testing.T) {
	var p Plugin = &stubPlugin{name: "maas", version: "v0.1.0"}
	assert.Equal(t, "maas", p.Manifest().Name)
	assert.Equal(t, "v0.1.0", p.Manifest().Version)
}
