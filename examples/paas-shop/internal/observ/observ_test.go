package observ

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// 请求染色不能覆盖独立的实例部署泳道证据；验证真实 Init + 中间件 + span 导出链。
func TestInstanceLaneSurvivesRequestColoring(t *testing.T) {
	for _, deploymentLane := range []string{"", "preview-instance"} {
		t.Run("deployment="+deploymentLane, func(t *testing.T) {
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-protobuf")
				w.WriteHeader(http.StatusOK)
			}))
			defer collector.Close()
			t.Setenv("PAAS_OTEL_ENDPOINT", strings.TrimPrefix(collector.URL, "http://"))
			t.Setenv("PAAS_LANE_ID", deploymentLane)
			t.Setenv("PAAS_ENV_TYPE", "test")
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "")
			oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
			defer otel.SetTracerProvider(oldProvider)
			defer otel.SetTextMapPropagator(oldPropagator)
			shutdown := Init("audit-service")
			defer shutdown()
			tp := otel.GetTracerProvider().(*sdktrace.TracerProvider)
			recorder := tracetest.NewSpanRecorder()
			tp.RegisterSpanProcessor(recorder)
			ctx, span := tp.Tracer("observ-test").Start(context.Background(), "request")
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			req.Header.Set(LaneHeader, "feature-request")
			LaneMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := LaneFromCtx(r.Context()); got != "feature-request" {
					t.Errorf("请求染色 = %q", got)
				}
			})).ServeHTTP(httptest.NewRecorder(), req)
			span.End()
			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("记录 span 数 = %d，期望 1", len(spans))
			}
			// 模拟 Jaeger 解析的合并顺序：资源属性在前，请求 span 属性覆盖同名值。
			tags := map[string]string{}
			for _, attr := range spans[0].Resource().Attributes() {
				tags[string(attr.Key)] = attr.Value.AsString()
			}
			for _, attr := range spans[0].Attributes() {
				tags[string(attr.Key)] = attr.Value.AsString()
			}
			wantInstance := deploymentLane
			if wantInstance == "" {
				wantInstance = "default"
			}
			if tags["paas.instance_lane"] != wantInstance || tags["paas.lane"] != "feature-request" || tags["paas.env"] != "test" {
				t.Fatalf("实例部署信息与请求染色未独立保留：%v", tags)
			}
		})
	}
}
