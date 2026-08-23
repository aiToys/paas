// Package metrics 提供 core 控制面的业务指标埋点（自定义 prometheus.Registry，
// 隔离 controller-runtime 进程级指标）。
package metrics

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry 持有自定义 prometheus.Registry + 业务指标句柄。
type Registry struct {
	reg *prometheus.Registry

	httpReqs       *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
	infReqTotal    *prometheus.CounterVec
	infTokensTotal *prometheus.CounterVec
	infDuration    *prometheus.HistogramVec
}

// NewRegistry 创建自定义 Registry 并注册全部业务指标。
func NewRegistry() *Registry {
	r := &Registry{reg: prometheus.NewRegistry()}
	r.httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "平台 HTTP 请求总数（按 method/route/status）",
	}, []string{"method", "route", "status"})
	r.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "平台 HTTP 请求耗时分布",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	r.infReqTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paas_inference_requests_total", Help: "推理请求总数（按 tenant/model/status）",
	}, []string{"tenant", "model", "status"})
	r.infTokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paas_inference_tokens_total", Help: "推理 token 用量（direction=prompt/completion）",
	}, []string{"tenant", "model", "direction"})
	r.infDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "paas_inference_duration_seconds", Help: "推理请求耗时分布",
		// LLM 流式响应普遍 >10s（DefBuckets 上限），P95 会钳在 10s 系统性低估——
		// 自定义桶覆盖 0.1s~10min（流式长对话）。
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"tenant", "model"})
	for _, c := range [...]prometheus.Collector{r.httpReqs, r.httpDuration, r.infReqTotal, r.infTokensTotal, r.infDuration} {
		r.reg.MustRegister(c)
	}
	return r
}

// Prometheus 返回底层 prometheus.Registry（供 /metrics handler）。
func (r *Registry) Prometheus() *prometheus.Registry { return r.reg }

// Inference 返回推理指标的便捷记录器。
func (r *Registry) Inference() *InferenceMetrics {
	return &InferenceMetrics{reqs: r.infReqTotal, tokens: r.infTokensTotal, dur: r.infDuration}
}

// InferenceMetrics 封装推理指标记录（gateway 持有）。
type InferenceMetrics struct {
	reqs   *prometheus.CounterVec
	tokens *prometheus.CounterVec
	dur    *prometheus.HistogramVec
}

// RecordInference 记录一次推理的请求计数 / token / 耗时。
// nil 接收者安全（防御未初始化场景调用）。
func (m *InferenceMetrics) RecordInference(tenant, model, status string, promptTokens, completionTokens int, durationSec float64) {
	if m == nil {
		return
	}
	m.reqs.WithLabelValues(tenant, model, status).Inc()
	m.tokens.WithLabelValues(tenant, model, "prompt").Add(float64(promptTokens))
	m.tokens.WithLabelValues(tenant, model, "completion").Add(float64(completionTokens))
	m.dur.WithLabelValues(tenant, model).Observe(durationSec)
}

// Handler 返回 /metrics 的 http.Handler（promhttp 自带不可达容错）。
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		ErrorLog: log.New(log.Writer(), "metrics /metrics: ", log.Flags()),
	})
}
