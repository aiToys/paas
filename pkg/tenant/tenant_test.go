package tenant

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTenantRoundTrip(t *testing.T) {
	ctx := WithTenant(context.Background(), "t-acme")
	got, ok := TenantFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "t-acme", got)
}

func TestTenantMissing(t *testing.T) {
	_, ok := TenantFrom(context.Background())
	assert.False(t, ok)
}

func TestTenantEmptyRejected(t *testing.T) {
	_, ok := TenantFrom(WithTenant(context.Background(), ""))
	assert.False(t, ok)
}
