// Package real 提供 observability reader 的真实后端实现（Prometheus/Loki/Tempo HTTP API）。
// 纯 net/http + JSON；后端不可达返空切片 + 日志，不 panic、不 5xx（降级）。
package real

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// metricNameToPromQL 把领域 metric 名映射为 Prometheus metric 约定名（写入端归埋点切片）。
var metricNameToPromQL = map[string]string{
	observability.MetricCPU:       "paas_cpu_usage",
	observability.MetricMem:       "paas_mem_usage",
	observability.MetricRPS:       "paas_rps",
	observability.MetricLatency:   "paas_latency_p95_ms",
	observability.MetricErrorRate: "paas_error_rate",
}

// MetricsStore 调 Prometheus HTTP API 实现 MetricsReader。
type MetricsStore struct {
	promURL string
	client  *http.Client
}

// NewMetricsStore 创建 Prometheus 适配。promURL 为 Prometheus 根地址（如 http://prom:9090）。
func NewMetricsStore(promURL string) *MetricsStore {
	return &MetricsStore{promURL: promURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// promResponse 是 Prometheus /api/v1/query_range 响应的最小子集。
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"` // [ts, "value"]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// ListMetrics 调 Prometheus 查最近 1h 时序，按领域维度映射为 []MetricSeries。
func (s *MetricsStore) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	// 无 name 时查全部约定 metric；否则查单个。
	names := []string{name}
	if name == "" {
		for n := range metricNameToPromQL {
			names = append(names, n)
		}
	}
	now := time.Now()
	end := now.Unix()
	start := now.Add(-time.Hour).Unix()
	step := int64(time.Hour.Seconds() / float64(observability.MaxPoints)) // ~60s，对齐 MaxPoints
	for _, mn := range names {
		promMetric, ok := metricNameToPromQL[mn]
		if !ok {
			// 非约定 metric 名：原样用作 PromQL metric（允许扩展）。
			promMetric = mn
		}
		q := promMetric
		if targetType != "" {
			q += fmt.Sprintf("{target_type=%q", targetType)
			if targetID != "" {
				q += fmt.Sprintf(",target_id=%q", targetID)
			}
			q += "}"
		}
		v := url.Values{}
		v.Set("query", q)
		v.Set("start", strconv.FormatInt(start, 10))
		v.Set("end", strconv.FormatInt(end, 10))
		v.Set("step", strconv.FormatInt(step, 10))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query_range?"+v.Encode(), nil)
		if err != nil {
			log.Printf("observability real metrics: 构造请求失败: %v", err)
			continue
		}
		resp, err := fetchJSON[promResponse](s.client, req)
		if err != nil {
			log.Printf("observability real metrics: 调 Prometheus 失败: %v", err)
			continue
		}
		pr := resp
		if pr.Status != "success" {
			log.Printf("observability real metrics: Prometheus 返回非 success: %s", pr.Error)
			continue
		}
		for _, r := range pr.Data.Result {
			out = append(out, toMetricSeries(r.Metric, r.Values))
		}
	}
	return out, nil
}

// toMetricSeries 把 Prometheus matrix 结果转为领域 MetricSeries。
func toMetricSeries(metric map[string]string, values [][]any) observability.MetricSeries {
	name := metric["__name__"]
	// 反查领域名（找不到保留原 PromQL 名）。
	for domain, prom := range metricNameToPromQL {
		if prom == name {
			name = domain
			break
		}
	}
	pts := make([]observability.MetricPoint, 0, len(values))
	var current float64
	for _, v := range values {
		if len(v) < 2 {
			continue
		}
		ts, _ := toFloat(v[0])
		val, _ := toFloat(v[1])
		pts = append(pts, observability.MetricPoint{TS: time.Unix(int64(ts), 0), Value: val})
		current = val
	}
	return observability.MetricSeries{
		ID:         fmt.Sprintf("%s|%s|%s", metric["target_type"], metric["target_id"], name),
		TargetType: metric["target_type"],
		TargetID:   metric["target_id"],
		Name:       name,
		Current:    current,
		Points:     pts,
	}
}

// toFloat 把 Prometheus 值（string 或 float64）转为 float64。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	case float64:
		return x, true
	}
	return 0, false
}
