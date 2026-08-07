package tool

import (
	"context"
	"time"
)

// withTimeout 包装 context.WithTimeout（工具调用/MCP 请求超时控制）。
func withTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
