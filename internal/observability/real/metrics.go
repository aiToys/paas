// Package real 提供 observability reader 的真实后端实现（Prometheus/Loki/Jaeger HTTP API）。
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

	"golang.org/x/sync/errgroup"

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
	promURL  string
	client   *http.Client
	lister   observability.AppWorkloadLister  // 应用级查询：解析 app→工作负载 ID（pod 名正则）
	entities observability.TenantEntityLister // 全局查询：列出租户全部应用/数据服务（健康矩阵）
}

// NewMetricsStore 创建 Prometheus 适配。promURL 为 Prometheus 根地址（如 http://prom:9090）。
// lister 可为 nil（应用级查询降级返空，不影响 dataservice/通用查询）。
// entities 可为 nil（全局查询降级返空，不影响 app/dataservice 维度）。
func NewMetricsStore(promURL string, lister observability.AppWorkloadLister, entities observability.TenantEntityLister) *MetricsStore {
	return &MetricsStore{promURL: promURL, client: httputil.NewClient(10 * time.Second), lister: lister, entities: entities}
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
	// 应用级：聚合该 app 下所有 Pod 的 cAdvisor + RED 指标（pod 名正则）。
	if targetType == observability.TargetApp {
		return s.listAppMetrics(ctx, targetID, name)
	}
	// 全局（targetType 空）：聚合租户内全部应用 + 全部数据服务（健康矩阵数据源）。
	// 与 memory 路径「空参数=租户全部 series」契约对齐。
	if targetType == "" {
		return s.listTenantMetrics(ctx, name)
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

// listTenantMetrics 全局聚合：租户内全部应用 + 全部数据服务的 series 拼接
// （可观测大屏「全部」视图健康矩阵数据源，与 memory「空参数=租户全部」契约对齐）。
// 并发查询（bounded errgroup）：可观测页 10s 轮询，串行 N+1 在实体多时拖垮响应
// （审计第 1/2/7 轮）。实体级查询失败不影响其它实体（逐实体降级）；entities 未注入返空。
func (s *MetricsStore) listTenantMetrics(ctx context.Context, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	if s.entities == nil {
		return out, nil
	}
	appIDs, err := s.entities.TenantAppIDs(ctx)
	if err != nil {
		log.Printf("observability real tenant metrics: 列应用失败: %v", err)
	}
	dsIDs, err := s.entities.TenantDataServiceIDs(ctx)
	if err != nil {
		log.Printf("observability real tenant metrics: 列数据服务失败: %v", err)
	}
	ids := make([]string, 0, len(appIDs)+len(dsIDs))
	kinds := make([]bool, 0, len(appIDs)+len(dsIDs)) // true=app, false=dataservice
	for _, id := range appIDs {
		ids = append(ids, id)
		kinds = append(kinds, true)
	}
	for _, id := range dsIDs {
		ids = append(ids, id)
		kinds = append(kinds, false)
	}
	results := make([][]observability.MetricSeries, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // 有界并发：防实体多时打爆 Prometheus
	for i := range ids {
		i := i
		g.Go(func() error {
			var series []observability.MetricSeries
			var err error
			if kinds[i] {
				series, err = s.listAppMetrics(gctx, ids[i], name)
			} else {
				series, err = s.listDataserviceMetrics(gctx, ids[i], name)
			}
			if err == nil {
				results[i] = series
			}
			return nil // 实体级失败降级（不取消其它实体）
		})
	}
	_ = g.Wait()
	for _, series := range results {
		out = append(out, series...)
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

// metricDef 描述一个领域指标的 Prometheus 查询（query_range 用）。
type metricDef struct {
	promQL, unit string
	scale        float64
}

// dataserviceDefs 构造数据服务 Pod 的全部指标 PromQL（按 ns + pod/dsID 限定，多租户隔离）。
// pod 用正则 <dsID>-\d+ 匹配全部副本（STS 扩容 N 副本后不只采集 ordinal 0，与 logs 路径语义一致）。
// 抽成函数供单测验证 PromQL 构造（real 模式无 fake Prometheus 测试基建）。
//
// 指标分两类标签过滤：
//  1. 容器级（cAdvisor/kubelet）：按 pod=<dsID>-0 过滤（cAdvisor 带 pod label）。
//  2. 引擎业务指标（exporter 自产）：不带 pod label，按 paas_aitoys_dataservice=<dsID>
//     过滤（prometheus scrape relabel 注入此 label，见 prometheus-values.yaml）。
//
// 指标来源：
//  1. 容器级（cAdvisor）：CPU/内存/磁盘IO/网络IO，container="main"（引擎主容器）。
//  2. PVC 用量（kubelet_volume_stats）：无需 exporter，PVC 名 = data-<pod>。
//  3. 引擎业务指标：sidecar exporter（container=exporter）或引擎内置 /metrics。
//     全查所有引擎指标名，Prometheus 无该 metric → 返空 series（诚实降级，前端按 kind 选展示）。
func dataserviceDefs(ns, pod, dsID string) map[string]metricDef {
	return map[string]metricDef{
		observability.MetricCPU: {fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{namespace=%q,pod=~%q,container=\"main\"}[5m]))", ns, podRegex(pod)), "cores", 1},
		observability.MetricMem: {fmt.Sprintf("sum(container_memory_working_set_bytes{namespace=%q,pod=~%q,container=\"main\"})", ns, podRegex(pod)), "MiB", 1.0 / 1048576},
		// 磁盘 IO：读+写速率合计（container_fs 按 container 维度，仅 main 容器）。
		observability.MetricDiskIO: {fmt.Sprintf("sum(rate(container_fs_reads_bytes_total{namespace=%q,pod=~%q,container=\"main\"}[5m]) + rate(container_fs_writes_bytes_total{namespace=%q,pod=~%q,container=\"main\"}[5m]))", ns, podRegex(pod), ns, podRegex(pod)), "KB/s", 1.0 / 1024},
		// 网络 IO：收+发速率合计（container_network 在 pod 级，不带 container label）。
		observability.MetricNetIO: {fmt.Sprintf("sum(rate(container_network_receive_bytes_total{namespace=%q,pod=~%q}[5m]) + rate(container_network_transmit_bytes_total{namespace=%q,pod=~%q}[5m]))", ns, podRegex(pod), ns, podRegex(pod)), "KB/s", 1.0 / 1024},
		// PVC 磁盘使用率 %（kubelet volume metrics，无需 exporter；PVC 名 data-<pod>）。
		observability.MetricDiskUsage: {fmt.Sprintf("max(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=~%q} / clamp_min(kubelet_volume_stats_capacity_bytes{namespace=%q,persistentvolumeclaim=~%q}, 1) * 100)", ns, "data-"+podRegex(pod), ns, "data-"+podRegex(pod)), "%", 1},
		// 引擎业务指标：exporter 自产不带 pod label，按 paas_aitoys_dataservice=<dsID> 过滤
		// （prometheus relabel 注入）。全查所有引擎指标名，无该 metric → 返空降级。
		// connections：DB 连接数 / 缓存连接数 / MQ 连接数（pg/redis/mysql/nats）。
		observability.MetricConnections: {fmt.Sprintf("max(pg_stat_activity_count{namespace=%q,paas_aitoys_dataservice=%q,state=\"active\"}) or max(redis_connected_clients{namespace=%q,paas_aitoys_dataservice=%q}) or max(mysql_threads_connected{namespace=%q,paas_aitoys_dataservice=%q}) or max(gnatsd_connz_num_connections{namespace=%q,paas_aitoys_dataservice=%q})", ns, dsID, ns, dsID, ns, dsID, ns, dsID), "", 1},
		// qps：DB 事务 / 缓存命令（pg/redis）。
		observability.MetricQPS: {fmt.Sprintf("sum(rate(pg_stat_database_xact_commit{namespace=%q,paas_aitoys_dataservice=%q}[5m])) or sum(rate(redis_commands_processed_total{namespace=%q,paas_aitoys_dataservice=%q}[5m]))", ns, dsID, ns, dsID), "/s", 1},
		// hit_rate：缓存命中率（redis exporter）。
		observability.MetricHitRate: {fmt.Sprintf("redis_keyspace_hit_rate{namespace=%q,paas_aitoys_dataservice=%q}", ns, dsID), "%", 1},
		// lag：MQ 消息速率（nats gnatsd 入站消息）。
		observability.MetricLag: {fmt.Sprintf("sum(rate(gnatsd_connz_in_msgs{namespace=%q,paas_aitoys_dataservice=%q}[5m]))", ns, dsID), "/s", 1},
		// vectors：向量数（qdrant 内置 /metrics）。
		observability.MetricVectors: {fmt.Sprintf("qdrant_collection_vectors_count{namespace=%q,paas_aitoys_dataservice=%q}", ns, dsID), "", 1},
	}
}

// listDataserviceMetrics 查数据服务 Pod 的 cAdvisor 指标（CPU 核数 / 内存 MiB）。
// targetID = 数据服务 K8s 资源名（领域 ID）；StatefulSet Pod 名 = <targetID>-0。
// 含 PVC 用量 + 引擎业务指标（详见 dataserviceDefs）；后端不可达返空切片 + 日志（降级，不 5xx）。
func (s *MetricsStore) listDataserviceMetrics(ctx context.Context, targetID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	if targetID == "" {
		return out, nil // 数据服务需指定 targetID
	}
	tid, _ := tenant.TenantFrom(ctx)
	ns := tenant.Namespace(tid) // paas-<tenant>，多租户隔离：防跨租户同名 Pod 串数据
	pod := targetID + "-0"
	defs := dataserviceDefs(ns, pod, targetID)
	names := []string{name}
	if name == "" {
		names = []string{
			observability.MetricCPU, observability.MetricMem, observability.MetricDiskIO, observability.MetricNetIO,
			observability.MetricDiskUsage,
			observability.MetricConnections, observability.MetricQPS, observability.MetricHitRate, observability.MetricLag, observability.MetricVectors,
		}
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

// listAppMetrics 聚合某应用下所有工作负载 Pod 的指标（计算 + RED 流量健康）。
//
// 计算指标（CPU/内存）：cAdvisor 在 node 级抓取，不带 Pod 自定义 label——无法按 paas_aitoys_app 直接过滤，
// 改按「工作负载 pod 名正则」聚合（AppWorkloadLister 解析 app→工作负载 ID，Deployment 名 = wl-<id>，
// Pod = <id>-<rsHash>-<podHash>），PromQL 用 `pod=~"wl-<id>-.*|..."` + namespace=paas-<tenant> 隔离。
//
// RED 指标（RPS/延迟/错误率）：依赖应用自身在业务端口暴露 /metrics（paas-shop observ.Handler 经
// promhttp 自动产 http_requests_total / http_request_duration_seconds）+ controller 注 prometheus.io/scrape
// 注解让 Prometheus 自动发现抓取。PromQL 同样按 ns + pod 正则聚合。
// 未暴露 /metrics 的应用 → RED PromQL 返空 series（前端卡片不出现，与 cpu/mem 同款降级）。
//
// 降级：lister 未注入 / app 无工作负载 / appID 空 → 返空切片（不报错）。
func (s *MetricsStore) listAppMetrics(ctx context.Context, appID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	if appID == "" || s.lister == nil {
		return out, nil // 应用级需指定 appID + lister 注入
	}
	ids, err := s.lister.AppWorkloadIDs(ctx, appID)
	if err != nil || len(ids) == 0 {
		return out, nil // app 无工作负载 / lister 错误：降级返空
	}
	tid, _ := tenant.TenantFrom(ctx)
	ns := tenant.Namespace(tid) // paas-<tenant>，多租户隔离（空 tid 兜底 paas-x）
	// Pod 名正则：wl-<id1>-.* | wl-<id2>-.* （Deployment 名 = wl-<id>，Pod = <deploy>-<hash>-<hash>）。
	podRegex := appPodRegex(ids)
	// cAdvisor 指标限定本租户 ns + 本应用 pod 名正则 + 排除 pause/空容器。
	lbl := fmt.Sprintf(`{namespace=%q,pod=~%q,container!="POD",container!=""}`, ns, podRegex)
	// 应用自暴露的 RED 指标（http_requests_total / http_request_duration_seconds）按 ns + pod 正则聚合。
	// 依赖：controller 给 service Pod 注 prometheus.io/scrape 注解（Prometheus kubernetes-pods job 发现）+
	// 应用在业务端口暴露 /metrics（paas-shop 经 observ.Handler 双重包装 promhttp 自动产 RED）。
	// 未暴露 /metrics 的应用 → 这些 PromQL 返空 series（前端卡片不出现，与 cpu/mem 同款降级）。
	redLbl := fmt.Sprintf(`{namespace=%q,pod=~%q}`, ns, podRegex)
	// 5xx selector：redLbl 内联 code=~"5.."（不可再开 { }，否则两个连续 selector 语法错）。
	redLbl5xx := fmt.Sprintf(`{namespace=%q,pod=~%q,code=~"5.."}`, ns, podRegex)
	defs := map[string]metricDef{
		observability.MetricCPU: {fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total%s[5m]))", lbl), "cores", 1},
		observability.MetricMem: {fmt.Sprintf("sum(container_memory_working_set_bytes%s)", lbl), "MiB", 1.0 / 1048576},
		// RED · Rate：每秒请求数（sum 全部 code）。
		observability.MetricRPS: {fmt.Sprintf("sum(rate(http_requests_total%s[5m]))", redLbl), "req/s", 1},
		// RED · Duration：P95 延迟（histogram_quantile over bucket rate，ms）。
		observability.MetricLatency: {fmt.Sprintf("histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket%s[5m]))) * 1000", redLbl), "ms", 1},
		// RED · Error：5xx 占比（防除零 clamp_min）；`or vector(0)` 让无 5xx 时也返 0%
		// 而非空 series——错误率面板应总可见（0% = 健康，缺卡易误判监控失效）。
		observability.MetricErrorRate: {fmt.Sprintf("(sum(rate(http_requests_total%s[5m])) or on() vector(0)) / clamp_min(sum(rate(http_requests_total%s[5m])), 1) * 100", redLbl5xx, redLbl), "%", 1},
	}
	names := []string{name}
	if name == "" {
		names = []string{observability.MetricCPU, observability.MetricMem, observability.MetricRPS, observability.MetricLatency, observability.MetricErrorRate}
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
