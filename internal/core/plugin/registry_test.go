package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/plugin"
)

func newStub(name string, deps ...string) plugin.Plugin {
	return &stub{name: name, deps: deps}
}

type stub struct {
	name string
	deps []string
}

func (s *stub) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: s.name, Depends: s.deps}
}
func (s *stub) Routes() []plugin.RouteSpec                  { return nil }
func (s *stub) Schemas() []plugin.CRDSchema                 { return nil }
func (s *stub) Meters() []plugin.MeterSpec                  { return nil }
func (s *stub) Init(context.Context, plugin.CoreDeps) error { return nil }
func (s *stub) Run(context.Context) error                   { return nil }

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	p := newStub("maas")
	require.NoError(t, r.Register(p))

	got, ok := r.Get("maas")
	assert.True(t, ok)
	assert.Equal(t, "maas", got.Manifest().Name)
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas")))
	err := r.Register(newStub("maas"))
	assert.Error(t, err)
}

func TestLoadOrderResolvesDeps(t *testing.T) {
	// maas 依赖 base；期望加载顺序为 [base, maas]
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas", "base")))
	require.NoError(t, r.Register(newStub("base")))

	order, err := r.LoadOrder()
	require.NoError(t, err)
	require.Len(t, order, 2)
	assert.Equal(t, "base", order[0].Manifest().Name)
	assert.Equal(t, "maas", order[1].Manifest().Name)
}

func TestLoadOrderDetectsMissingDep(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas", "ghost")))
	_, err := r.LoadOrder()
	assert.Error(t, err)
}

func TestLoadOrderDetectsCycle(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("a", "b")))
	require.NoError(t, r.Register(newStub("b", "a")))
	_, err := r.LoadOrder()
	assert.Error(t, err)
}
