//go:build otel

// lane_otel.go OTel span 桥接（build tag otel 门控）。
//
// 用法：应用引了 OTel 且想让 trace 携带泳道标识时，启动时调一次：
//
//	paasregistry.SetLaneSpanHook(paasregistry.OTelLaneHook)
//
// 之后 LaneMiddleware 提取到非 default lane 时会写 span 属性 paas.lane=<lane>，
// trace（Jaeger）中即可按泳道过滤 span——泳道排障（同服务多泳道并行时区分流量归属）。
// 未接 OTel 的应用不 import 本文件（无 build tag），SDK 零 OTel 依赖。
package paasregistry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// OTelLaneHook 把 lane 写进当前 ctx 的 span 属性（SpanFromContext，无 span 时静默跳过）。
// 仅标记属性（无新 span / 无 end 语义），返回 nil 结束回调。
func OTelLaneHook(ctx context.Context, lane string) func() {
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String(LaneAttrKey, lane))
	}
	return nil
}
