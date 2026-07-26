package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aitoys/paas/pkg/provider"
)

type fakeProvider struct{ name string }

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Chat(_ context.Context, _ provider.ChatRequest) (<-chan provider.Chunk, error) {
	return nil, nil
}

func TestRegisterAndGet(t *testing.T) {
	g := New()
	g.Register("echo", fakeProvider{name: "echo"})

	p, ok := g.Get("echo")
	assert.True(t, ok)
	assert.Equal(t, "echo", p.Name())

	_, ok = g.Get("ghost")
	assert.False(t, ok)
}

func TestModels(t *testing.T) {
	g := New()
	g.Register("a", fakeProvider{name: "a"})
	g.Register("b", fakeProvider{name: "b"})
	assert.ElementsMatch(t, []string{"a", "b"}, g.Models())
}
