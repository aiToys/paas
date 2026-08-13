// Package tracing 初始化 OpenTelemetry tracer provider，向 OTLP 兼容后端
// （Tempo / Jaeger / OTel Collector）上报链路。
//
// 设计：env 开关，PAAS_OTEL_ENDPOINT 为空时返回 noop provider（行为与无 OTel 完全一致，
// 零依赖运行不破坏现状）；非空时建 OTLP/HTTP exporter + BatchSpanProcessor。
// 调用方在进程退出时执行返回的 shutdown 以 flush 未发送 span。
package tracing

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// ServiceName 上报的服务名（控制面统一标识）。
const ServiceName = "paas-core"

// Init 初始化全局 TracerProvider。
//   - endpoint 形如 "localhost:4318"（OTLP/HTTP，默认 grpc→http 端口 4318）；
//     空串则不初始化（noop），返回的 shutdown 为 no-op。
//   - 返回的 shutdown 必须在进程退出前调用以 flush span。
func Init(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		// noop：otel 全局默认即 noop tracer，不接入任何后端。
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		// 开发/内网直连 Collector 或 Jaeger，不走 TLS（生产经 Collector 终结 TLS）。
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(ServiceName),
		))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(2*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		// AlwaysSample：控制面流量低，全采样便于联调；高流量生产改 ParentBased/TraceIDRatio。
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C traceparent（跨服务透传）
		propagation.Baggage{},
	))

	log.Printf("[otel] tracer provider 已初始化，endpoint=%s service=%s", endpoint, ServiceName)
	return tp.Shutdown, nil
}
