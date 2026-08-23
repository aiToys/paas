// range.go 查询时间范围 ctx 携带（时间范围选择器，R3-I1）。
//
// 不改 Repository 接口签名（metrics/logs/traces 三 reader × memory/compose/real 多实现的
// 面太大）；range 作为查询元数据走 ctx，real 适配层取出计算 start/end（默认 1h 保持现状）。
// memory 惰性路径忽略 range（无真实时序概念）。
package observability

import (
	"context"
	"time"
)

type rangeKey struct{}

// 默认查询范围与允许值域（15m/1h/6h/24h，与前端选择器对齐）。
const (
	DefaultRange = time.Hour
	MinRange     = 15 * time.Minute
	MaxRange     = 24 * time.Hour
)

// WithRange 把查询时间范围注入 ctx（超出值域钳到边界；d<=0 用默认）。
func WithRange(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		d = DefaultRange
	}
	if d < MinRange {
		d = MinRange
	}
	if d > MaxRange {
		d = MaxRange
	}
	return context.WithValue(ctx, rangeKey{}, d)
}

// RangeFrom 取 ctx 中的查询范围（未注入返 DefaultRange）。
func RangeFrom(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(rangeKey{}).(time.Duration); ok && d > 0 {
		return d
	}
	return DefaultRange
}

// ParseRange 解析 HTTP range 参数（"15m"/"1h"/"6h"/"24h"），空串返 0（调用方 WithRange 兜底默认）。
func ParseRange(s string) time.Duration {
	switch s {
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "24h":
		return 24 * time.Hour
	}
	return 0
}
