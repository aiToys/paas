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

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
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
	return &MetricsStore{promURL: promURL, client: httputil.NewClient(10 * time.Second)}
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
	// 数据服务：Pod 级真实指标走 cAdvisor（container_*），不依赖 paas_* 埋点。
	if targetType == observability.TargetDataservice {
		return s.listDataserviceMetrics(ctx, targetID, name)
	}
	// 应用级：聚合该 app 下所有 Pod 的 cAdvisor 指标（按 paas_aitoys_app label）。
	// 仅 CPU/内存（应用级 RPS/latency 无数据源——PaaS 不代理应用流量）。
	if targetType == observability.TargetApp {
		return s.listAppMetrics(ctx, targetID, name)
	}
	out := make([]observability.MetricSeries, 0)
	// 无 name 时查全部约定 metric；否则查单个。
	names := []string{}
	if name != "" {
		names = append(names, name)
	} else {
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

// listDataserviceMetrics 查数据服务 Pod 的 cAdvisor 指标（CPU 核数 / 内存 MiB）。
// targetID = 数据服务 K8s 资源名（领域 ID）；StatefulSet Pod 名 = <targetID>-0。
// 仅 CPU/内存（数据服务无通用 RPS/延迟埋点）；后端不可达返空切片 + 日志（降级，不 5xx）。
func (s *MetricsStore) listDataserviceMetrics(ctx context.Context, targetID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	if targetID == "" {
		return out, nil // 数据服务需指定 targetID
	}
	pod := targetID + "-0"
	type metricDef struct {
		promQL, unit string
		scale        float64
	}
	defs := map[string]metricDef{
		observability.MetricCPU: {fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{pod=%q,container=\"main\"}[5m]))", pod), "cores", 1},
		observability.MetricMem: {fmt.Sprintf("container_memory_working_set_bytes{pod=%q,container=\"main\"}", pod), "MiB", 1.0 / 1048576},
	}
	names := []string{name}
	if name == "" {
		names = []string{observability.MetricCPU, observability.MetricMem}
	}
	now := time.Now()
	end := now.Unix()
	start := now.Add(-time.Hour).Unix()
	step := int64(time.Hour.Seconds() / float64(observability.MaxPoints))
	for _, mn := range names {
		def, ok := defs[mn]
		if !ok {
			continue // 数据服务不支持该指标（如 rps/latency）
		}
		v := url.Values{}
		v.Set("query", def.promQL)
		v.Set("start", strconv.FormatInt(start, 10))
		v.Set("end", strconv.FormatInt(end, 10))
		v.Set("step", strconv.FormatInt(step, 10))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query_range?"+v.Encode(), nil)
		if err != nil {
			log.Printf("observability real dataservice metrics: 构造请求失败: %v", err)
			continue
		}
		resp, err := fetchJSON[promResponse](s.client, req)
		if err != nil {
			log.Printf("observability real dataservice metrics: 调 Prometheus 失败: %v", err)
			continue
		}
		if resp.Status != "success" {
			log.Printf("observability real dataservice metrics: Prometheus 返回非 success: %s", resp.Error)
			continue
		}
		for _, r := range resp.Data.Result {
			pts := make([]observability.MetricPoint, 0, len(r.Values))
			var current float64
			for _, p := range r.Values {
				if len(p) < 2 {
					continue
				}
				ts, _ := toFloat(p[0])
				val, _ := toFloat(p[1])
				scaled := val * def.scale
				pts = append(pts, observability.MetricPoint{TS: time.Unix(int64(ts), 0), Value: scaled})
				current = scaled
			}
			out = append(out, observability.MetricSeries{
				ID:         fmt.Sprintf("dataservice|%s|%s", targetID, mn),
				TargetType: observability.TargetDataservice,
				TargetID:   targetID,
				Name:       mn,
				Unit:       def.unit,
				Current:    current,
				Points:     pts,
			})
		}
	}
	return out, nil
}

// listAppMetrics 聚合某应用下所有工作负载 Pod 的 cAdvisor 指标（CPU 核数 / 内存 MiB）。
//
// workload_controller 给工作负载 Pod 打 label `paas.aitoys/app=<appID>`（promtail/cAdvisor
// 原样保留该 label），故 cAdvisor 指标可按 `paas_aitoys_app` label 过滤 + sum 聚合。
// 排除 pause 容器（container="POD"）和空容器名，避免重复计数。
//
// 仅 CPU/内存：应用级 RPS/latency 无数据源（PaaS 平台不代理应用业务流量，无 ingress
// metrics），留后续接应用网关 metrics 时补；当前 name 传 rps/latency 返空（降级）。
func (s *MetricsStore) listAppMetrics(ctx context.Context, appID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	if appID == "" {
		return out, nil // 应用级需指定 appID
	}
	type metricDef struct {
		promQL, unit string
		scale        float64
	}
	// label 过滤：paas_aitoys_app=<appID> + 排除 pause/空容器 + 多租户隔离（paas_aitoys_tenant）。
	lbl := fmt.Sprintf(`{paas_aitoys_app=%q,container!="POD",container!=""`, appID)
	if tid, ok := tenant.TenantFrom(ctx); ok && tid != "" {
		lbl += fmt.Sprintf(`,paas_aitoys_tenant=%q`, tid)
	}
	lbl += "}"
	defs := map[string]metricDef{
		observability.MetricCPU: {fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total%s[5m]))", lbl), "cores", 1},
		observability.MetricMem: {fmt.Sprintf("sum(container_memory_working_set_bytes%s)", lbl), "MiB", 1.0 / 1048576},
	}
	names := []string{name}
	if name == "" {
		names = []string{observability.MetricCPU, observability.MetricMem}
	}
	now := time.Now()
	end := now.Unix()
	start := now.Add(-time.Hour).Unix()
	step := int64(time.Hour.Seconds() / float64(observability.MaxPoints))
	for _, mn := range names {
		def, ok := defs[mn]
		if !ok {
			continue // 应用级不支持该指标（rps/latency 无数据源）
		}
		v := url.Values{}
		v.Set("query", def.promQL)
		v.Set("start", strconv.FormatInt(start, 10))
		v.Set("end", strconv.FormatInt(end, 10))
		v.Set("step", strconv.FormatInt(step, 10))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query_range?"+v.Encode(), nil)
		if err != nil {
			log.Printf("observability real app metrics: 构造请求失败: %v", err)
			continue
		}
		resp, err := fetchJSON[promResponse](s.client, req)
		if err != nil {
			log.Printf("observability real app metrics: 调 Prometheus 失败: %v", err)
			continue
		}
		if resp.Status != "success" {
			log.Printf("observability real app metrics: Prometheus 返回非 success: %s", resp.Error)
			continue
		}
		for _, r := range resp.Data.Result {
			pts := make([]observability.MetricPoint, 0, len(r.Values))
			var current float64
			for _, p := range r.Values {
				if len(p) < 2 {
					continue
				}
				ts, _ := toFloat(p[0])
				val, _ := toFloat(p[1])
				scaled := val * def.scale
				pts = append(pts, observability.MetricPoint{TS: time.Unix(int64(ts), 0), Value: scaled})
				current = scaled
			}
			out = append(out, observability.MetricSeries{
				ID:         fmt.Sprintf("app|%s|%s", appID, mn),
				TargetType: observability.TargetApp,
				TargetID:   appID,
				Name:       mn,
				Unit:       def.unit,
				Current:    current,
				Points:     pts,
			})
		}
	}
	return out, nil
}
