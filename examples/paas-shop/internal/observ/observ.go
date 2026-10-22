// Package observ 提供 paas-shop 示例服务共享的可观测工具：OTel trace（OTLP -> Jaeger）
// + Prometheus metrics（/metrics）+ 结构化日志（slog）+ HTTP client/server 传播 trace。
//
// 设计：每个微服务（product/recommend/chatbot/bff）启动调 Init(serviceName) 初始化 tracer，
// Handler 包装业务路由自动建 span，NewClient 注入 traceparent 实现跨服务 trace 链路。
// PAAS_OTEL_ENDPOINT 由平台 controller 注入（jaeger.observability.svc:4318），空则 noop（本地 dev 可用）。
package observ

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// RED 指标（OpenTelemetry 语义约定：Rate/Error/Duration）。
// 平台 controller 给 service Pod 注 prometheus.io/scrape 注解后，Prometheus 自动抓 /metrics，
// 控制台 metrics.go 按 pod 正则聚合出应用级 RPS / 错误率 / P95 延迟。
var (
	httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP 请求总数（按状态码 code）",
	}, []string{"code"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP 请求耗时分布",
		Buckets: prometheus.DefBuckets,
	}, []string{})
)

func init() {
	prometheus.MustRegister(httpReqs, httpDuration)
}

// Init 初始化 OTel tracer（OTLP HTTP -> PAAS_OTEL_ENDPOINT）+ W3C tracecontext 传播器。
// endpoint 空（本地 dev）则 noop，不阻塞功能。返回 shutdown func，主函数 defer 调用。
func Init(serviceName string) func() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	endpoint := os.Getenv("PAAS_OTEL_ENDPOINT")
	if endpoint == "" {
		slog.Info("OTel trace noop（PAAS_OTEL_ENDPOINT 未设置）", "service", serviceName)
		return func() {}
	}
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(), // Jaeger ClusterIP 内部访问，无 TLS
	)
	if err != nil {
		slog.Error("otlp exporter 构造失败，trace noop", "err", err, "endpoint", endpoint)
		return func() {}
	}
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp),
		trace.WithResource(resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			// 排障上下文资源属性：平台 controller 注入 env（PAAS_TENANT_ID/PAAS_CLUSTER_ID/
			// PAAS_LANE_ID），每个 span 自动携带——trace 可按租户/集群/泳道过滤（泳道排障：
			// 同服务多泳道并行时区分流量归属）。缺省 env 兜底 default。
			attribute.String("paas.tenant", envOr("PAAS_TENANT_ID", "default")),
			attribute.String("paas.cluster", envOr("PAAS_CLUSTER_ID", "default")),
			attribute.String("paas.lane", envOr("PAAS_LANE_ID", "default")),
			// 独立保存实例部署泳道，避免 paas.lane 被请求染色覆盖后失去实例证据。
			attribute.String("paas.instance_lane", envOr("PAAS_LANE_ID", "default")),
			// 环境标识（setup.sh 注入 PAAS_ENV_TYPE）：test/prod 链路在 trace 视图一眼可分。
			attribute.String("paas.env", envOr("PAAS_ENV_TYPE", "unknown")),
		)),
	)
	otel.SetTracerProvider(tp)
	slog.Info("OTel tracer 就绪", "service", serviceName, "endpoint", endpoint)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
	}
}

// Handler 包装 http.Handler：先记录 RED 指标（promhttp 自动填 code = 状态码），
// 再用 otelhttp 自动建 span（含 http 语义属性 + 5xx 异常）。
// 每个服务的 mux 用此包装，所有请求自动 RED + trace + 访问日志。
// /metrics 和 /healthz 通过 skipTracePaths 跳过（不建 span 不打日志，避免高频噪音）。
func Handler(operation string, h http.Handler) http.Handler {
	h = promhttp.InstrumentHandlerCounter(httpReqs, h)
	h = promhttp.InstrumentHandlerDuration(httpDuration, h)
	// accessLog 在 otelhttp 内层：执行时 ctx 已带 span，可提取 trace_id。
	return otelhttp.NewHandler(accessLog(h), operation, otelhttp.WithFilter(skipTracePaths))
}

// statusWriter 记录下游写入的状态码（访问日志用；promhttp 的包装在外层不共享）。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// accessLog 请求访问日志：一行一条（method/path/status/耗时/trace_id）。
// trace_id 从当前请求 span 提取——平台「trace 展开 → 关联日志」按 traceId 匹配日志行
// （ListLogs 内存 best-effort：stream label 或行内文本），应用此前只在错误时打日志，
// 成功请求零日志可关联，trace 深链的日志面板永远为空（2026-10-21 实测）。
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !skipTracePaths(r) { // /metrics /healthz /api/events 不打（与 trace 同策略防刷屏）
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		tid := ""
		if sc := oteltrace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
			tid = sc.TraceID().String()
		}
		slog.Info("请求",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"durationMs", time.Since(start).Milliseconds(),
			"trace_id", tid,
		)
	})
}

// MetricsHandler 返回 /metrics handler（promhttp），供 Prometheus scrape。
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// NewClient 返回注入 traceparent 的 HTTP client（跨服务调用时 trace 传播）。
// 10s 超时适合普通 REST 调用；SSE 流式透传请用 NewStreamingClient（无整体超时）。
func NewClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}

// NewStreamingClient 返回无整体超时的 client（仍注入 traceparent），供 SSE 流式透传用。
// 流式场景（如客服 chat：第一次 LLM 决策含 reasoning 思考链，耗时可达数十秒）不能用
// 带整体 Timeout 的 client，否则长推理被 10s 截断 → context canceled。
func NewStreamingClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}

// LaneMiddleware 泳道染色入口中间件：提取 x-paas-lane header 写进当前请求 span 属性
// paas.lane + 存 ctx（下游经 LaneFromCtx 取，转发时透传 header）。
// 与平台 SDK（sdk/paas-registry）语义一致；examples 不引 paas 内部包故就地实现（标准库 only）。
// lane 缺省/default 不写属性（基线减少噪音），header 原样透传由调用方决定。
func LaneMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lane := r.Header.Get(LaneHeader)
		if lane == "" || lane == "default" {
			next.ServeHTTP(w, r)
			return
		}
		if span := oteltrace.SpanFromContext(r.Context()); span.IsRecording() {
			span.SetAttributes(attribute.String("paas.lane", lane))
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), laneCtxKey{}, lane)))
	})
}

// LaneHeader 流量染色 HTTP header 名（与平台 SDK/sdk/paas-registry 同值）。
const LaneHeader = "x-paas-lane"

// laneCtxKey ctx 携带 lane 的 key（私有类型防冲突）。
type laneCtxKey struct{}

// LaneFromCtx 从 ctx 取 lane；无/空返 ""。
func LaneFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(laneCtxKey{}).(string)
	return v
}

// LaneOrBase 从 ctx 取 lane，空/default 归一为 "default"（响应头展示用）。
func LaneOrBase(ctx context.Context) string {
	if lane := LaneFromCtx(ctx); lane != "" {
		return lane
	}
	return "default"
}

// ApplyLaneHeader 转发下游前从 ctx 取 lane 注入请求 header（跨服务染色透传）。
func ApplyLaneHeader(ctx context.Context, req *http.Request) {
	if lane := LaneFromCtx(ctx); lane != "" && lane != "default" {
		req.Header.Set(LaneHeader, lane)
	}
}

// Recover 中间件：捕获 panic 防 goroutine 崩溃，记日志 + 返 500。
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "rec", rec, "path", r.URL.Path)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// MiddlewareSpan 通用中间件调用 span：包装一次 DB/缓存/MQ 操作建子 span，
// 使瀑布图可见中间件耗时与目标（此前 pgx/go-redis/nats 直连是 trace 黑盒，
// bff 之后的 DB 访问在瀑布中不可见，无法排障「慢在哪」）。
//
// 用法：end := observ.MiddlewareSpan(ctx, "redis.get", ...); err := op(); end()
// 语义属性按 OTel 数据库/消息约定（db.system / db.statement / messaging.destination）。
// noop tracer（PAAS_OTEL_ENDPOINT 空）下零开销（span 不 recording）。
func MiddlewareSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) func() {
	_, span := otel.Tracer("paas-shop/middleware").Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return func() { span.End() }
}

// 语义属性常量（OTel database / messaging semantic conventions 摘要）。
const (
	AttrDBSystem    = attribute.Key("db.system")
	AttrDBStatement = attribute.Key("db.statement")
	AttrDBOperation = attribute.Key("db.operation")
	AttrMQDest      = attribute.Key("messaging.destination")
	AttrMQSystem    = attribute.Key("messaging.system")
)

// envOr 读 env，空返 fallback。
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ServiceFingerprint 服务指纹：返回处理本次请求的实例标识（Pod 名 + 镜像版本摘要）。
// 泳道演示用：响应头 X-Paas-Service 携带——前端切泳道后可见「实际命中的实例/版本」，
// 对比基线与泳道实例差异（Pod 名含 workload ID + RS hash，版本演进时 hash 变化可见）。
// 版本摘要取 PAAS_IMAGE_TAG env（平台注入 IMAGE_REF tag 时有效），空则仅 Pod 名。
func ServiceFingerprint() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	if tag := os.Getenv("PAAS_IMAGE_TAG"); tag != "" {
		return host + " @ " + tag
	}
	return host
}

// skipTracePaths 过滤无业务价值的高频端点，避免污染 trace（与 paas-core 同款策略）。
// /metrics：Prometheus scrape（15s/次，纯数据输出端点，trace 无意义且制造噪音）。
// /healthz：健康检查探针。
// /api/events：前端通知铃铛 10s 轮询（纯拉取端点，刷屏 trace 列表淹没真实请求）。
func skipTracePaths(r *http.Request) bool {
	switch r.URL.Path {
	case "/metrics", "/healthz", "/api/events":
		return false // 跳过（不建 span）
	}
	return true
}
