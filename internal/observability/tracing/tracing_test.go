package tracing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

func TestInitEmptyEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	// 空 endpoint 不替换全局 provider（保持默认 noop）。
	// 调用 shutdown 应为 no-op 不报错。
	assert.NoError(t, shutdown(context.Background()))
}

func TestInitWithExporterSetsGlobalProvider(t *testing.T) {
	// 不可达端点：Init 仍成功（exporter 懒连接，仅 shutdown 时报错），但应替换全局 provider。
	orig := otel.GetTracerProvider()
	shutdown, err := Init(context.Background(), "127.0.0.1:65535") // 不可达端口
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NotSame(t, orig, otel.GetTracerProvider(), "非空 endpoint 应设置全局 provider")
	// shutdown 尝试 flush 不可达端点会报错，但不 panic。
	_ = shutdown(context.Background())
}
