package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMeterAccumulates(t *testing.T) {
	m := &Meter{}
	m.Record("t1", "echo", 10)
	m.Record("t1", "echo", 5)
	assert.Equal(t, 15, m.Count())
}
