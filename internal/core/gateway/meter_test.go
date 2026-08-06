package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMeterAccumulates(t *testing.T) {
	m := &Meter{}
	m.Record("t1", "app-a", "echo", "researcher", 10)
	m.Record("t1", "", "echo", "", 5) // 租户级 Key（无 appID/user）也累计
	assert.Equal(t, 15, m.Count())
}
