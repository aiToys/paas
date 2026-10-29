// 诊断 raw 查询入口（spec tmp/diagnosis-agent/impl-spec.md S2.4–S2.6）。
//
// 与 metrics.go/logs.go/traces.go 的租户路径不同：本组方法收原始 PromQL/LogQL/
// service 名，**不做租户白名单过滤、不吞错误**（租户路径失败降级返空，诊断路径
// 必须区分 error 与 empty——四态分流的前提）。调用方（internal/diagnose/tools）
// 必须确保入口已过 adminGuard（super_admin 语境）。
package real

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// RawSample Prometheus instant vector 单样本。
type RawSample struct {
	Metric map[string]string `json:"metric"`
	Value  float64           `json:"value"`
	TS     float64           `json:"ts"` // Unix 秒
}

// RawSeries Prometheus range vector 单序列。
type RawSeries struct {
	Metric map[string]string `json:"metric"`
	Values []RawPoint        `json:"values"`
}

// RawPoint 时序单点。
type RawPoint struct {
	TS    float64 `json:"ts"`
	Value float64 `json:"value"`
}

// promInstantResponse 复用 quality.go 的定义（/api/v1/query 响应最小子集）。
// promRangeResponse 复用 metrics.go 的 promResponse 结构语义。
type promRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// QueryInstant 执行任意 PromQL 即时查询（诊断 T5/T6 数据源）。
// PromQL 语法错 / 上游不可达返 error（含 Prometheus errorType 摘要）；查询成功无数据
// 返空切片（调用方分流 empty）。
func (s *MetricsStore) QueryInstant(ctx context.Context, promql string) ([]RawSample, error) {
	v := url.Values{}
	v.Set("query", promql)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Prometheus 请求失败: %w", err)
	}
	resp, err := fetchJSON[promInstantResponse](s.client, req)
	if err != nil {
		return nil, fmt.Errorf("调 Prometheus 失败（不可达）")
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus 查询失败: %s", resp.Error)
	}
	out := make([]RawSample, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		ts, _ := toFloat(r.Value[0])
		val, _ := toFloat(r.Value[1])
		out = append(out, RawSample{Metric: r.Metric, Value: val, TS: ts})
	}
	return out, nil
}

// QueryRange 执行任意 PromQL 范围查询（诊断 T5 时间序列展开）。
// step 秒；start/end 为绝对时间。
func (s *MetricsStore) QueryRange(ctx context.Context, promql string, start, end time.Time, stepSec int) ([]RawSeries, error) {
	if stepSec <= 0 {
		stepSec = 30
	}
	v := url.Values{}
	v.Set("query", promql)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", strconv.Itoa(stepSec))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Prometheus 请求失败: %w", err)
	}
	resp, err := fetchJSON[promRangeResponse](s.client, req)
	if err != nil {
		return nil, fmt.Errorf("调 Prometheus 失败（不可达）")
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus 查询失败: %s", resp.Error)
	}
	out := make([]RawSeries, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		series := RawSeries{Metric: r.Metric, Values: make([]RawPoint, 0, len(r.Values))}
		for _, p := range r.Values {
			if len(p) < 2 {
				continue
			}
			ts, _ := toFloat(p[0])
			val, _ := toFloat(p[1])
			series.Values = append(series.Values, RawPoint{TS: ts, Value: val})
		}
		out = append(out, series)
	}
	return out, nil
}

// RawLogLine 诊断日志行（时间 + pod 归属 + 内容）。
type RawLogLine struct {
	TS   time.Time `json:"ts"`
	Pod  string    `json:"pod,omitempty"`
	Line string    `json:"line"`
}

// RawLoki 执行原始 LogQL 查询（诊断 T4 数据源）。
// logql 须为完整 LogQL（含 {namespace=...} selector，由调用方构造）；
// sinceMin 限时间窗；limit 截断。错误不吞（区分 error/empty 的前提）。
// unwrapDockerJSON 剥 docker 包装行（与租户路径同源）。
func (s *LogsStore) RawLoki(ctx context.Context, logql string, sinceMin, limit int) ([]RawLogLine, error) {

	now := time.Now()
	if sinceMin <= 0 {
		sinceMin = 60
	}
	return s.RawLokiWindow(ctx, logql, now.Add(-time.Duration(sinceMin)*time.Minute), now, limit)
}

// RawLokiWindow 按固定绝对窗口查询，避免后台排队和工具循环使证据时间漂移。
func (s *LogsStore) RawLokiWindow(ctx context.Context, logql string, start, end time.Time, limit int) ([]RawLogLine, error) {
	if s == nil {
		return nil, fmt.Errorf("日志源未配置")
	}
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	v := url.Values{}
	v.Set("query", logql)
	v.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	v.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	v.Set("limit", strconv.Itoa(limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lokiURL+"/loki/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Loki 请求失败: %w", err)
	}
	lr, err := fetchJSON[lokiResponse](s.client, req)
	if err != nil {
		return nil, fmt.Errorf("调 Loki 失败（不可达）")
	}
	if lr.Status != "success" {
		return nil, fmt.Errorf("loki 查询失败: %s", lr.Error)
	}
	out := make([]RawLogLine, 0, limit)
	for _, r := range lr.Data.Result {
		pod := r.Stream["pod"]
		for _, val := range r.Values {
			if len(val) < 2 {
				continue
			}
			tsNs, _ := strconv.ParseInt(val[0], 10, 64)
			out = append(out, RawLogLine{TS: time.Unix(0, tsNs), Pod: pod, Line: unwrapDockerJSON(val[1])})
		}
	}
	return out, nil
}

// QueryTracesRaw 按 service/operation/minDurMs/sinceMin 查 Jaeger（诊断 T7 数据源）。
// 与租户路径 ListTraces 的差异：无租户服务白名单（平台诊断可查任意 service）、
// 支持 operation 与最小耗时过滤、错误不吞。service 空拒查（防全量枚举）。
func (s *TracesStore) QueryTracesRaw(ctx context.Context, service, operation string, minDurMs int64, sinceMin, limit int) ([]observability.Trace, error) {
	if service == "" {
		return nil, fmt.Errorf("service 不能为空")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now()
	if sinceMin <= 0 {
		sinceMin = 60
	}
	v := url.Values{}
	v.Set("service", service)
	if operation != "" {
		v.Set("operation", operation)
	}
	if minDurMs > 0 {
		// Jaeger minDuration 接受 Go duration 形式（如 "300ms"）。
		v.Set("minDuration", (time.Duration(minDurMs) * time.Millisecond).String())
	}
	v.Set("start", strconv.FormatInt(now.Add(-time.Duration(sinceMin)*time.Minute).UnixMicro(), 10))
	v.Set("end", strconv.FormatInt(now.UnixMicro(), 10))
	v.Set("limit", strconv.Itoa(limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jaegerURL+"/api/traces?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Jaeger 请求失败: %w", err)
	}
	resp, err := fetchJSON[jaegerTraceResponse](s.client, req)
	if err != nil {
		return nil, fmt.Errorf("调 Jaeger 失败（不可达）")
	}
	// Jaeger 查询无 errors 字段：HTTP 200 + data 数组即成功；解析失败由 json decode 捕获。
	out := make([]observability.Trace, 0, len(resp.Data))
	for _, jt := range resp.Data {
		tr, hasErr := parseJaegerTrace(jt)
		if hasErr {
			tr.Status = observability.TraceError
		}
		out = append(out, tr)
	}
	return out, nil
}
