// 采集质量探测（spec 2026-10-17 专项04 §5）：逐约定 metric 发 Prometheus 即时查询
// （/api/v1/query 单点，比 query_range 轻），区分「查询成功无数据」（引导检查时间
// 范围/目标/埋点）与「查询失败」（采集源问题）——两者混谈会把「未接入埋点」误报
// 为平台故障。PromQL 与主查询同源（podSeriesDefs/dataserviceDefs 复用），探测结论
// 对主查询路径成立。
package real

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// errProbeTarget 探测目标缺失/类型不支持（handler 映射 400）。
var errProbeTarget = errors.New("探测目标缺失或类型不支持（需 targetType=app/workload/dataservice 且 targetId 非空）")

// promInstantResponse 是 Prometheus /api/v1/query（即时查询）响应的最小子集。
type promInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"` // [ts, "value"]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// ProbeQuality 对 (targetType, targetID) 逐约定 metric 即时探测。
// 错误脱敏：网络/HTTP 层错误只回报「连接采集源失败」分类（err 可能含上游 URL，
// 不透传）；Prometheus 自身 error（如 PromQL 语法）无 URL 可保留定位价值。
func (s *MetricsStore) ProbeQuality(ctx context.Context, targetType, targetID string) (observability.QualityReport, error) {
	if targetType == "" {
		return observability.QualityReport{
			Source: observability.QualitySourceProm,
			Note:   "全局视图的采集覆盖矩阵留后续，请按应用/工作负载/数据服务维度探测",
		}, nil
	}
	tid, _ := tenant.TenantFrom(ctx)
	ns := tenant.Namespace(tid)
	var defs map[string]metricDef
	switch targetType {
	case observability.TargetDataservice:
		if targetID == "" {
			return observability.QualityReport{}, errProbeTarget
		}
		defs = dataserviceDefs(ns, targetID+"-0", targetID)
	case observability.TargetWorkload:
		if targetID == "" {
			return observability.QualityReport{}, errProbeTarget
		}
		defs = podSeriesDefs(ns, targetID+".*")
	case observability.TargetApp:
		if targetID == "" || s.lister == nil {
			return observability.QualityReport{}, errProbeTarget
		}
		ids, err := s.lister.AppWorkloadIDs(ctx, targetID)
		if err != nil || len(ids) == 0 {
			// 应用无工作负载：无法构成查询目标。empty 语义（非 failed）——
			// 查询本身没失败，是这个应用当前没有可采集对象。
			return observability.QualityReport{
				Source: observability.QualitySourceProm,
				Note:   "该应用暂无工作负载，无采集目标",
			}, nil
		}
		defs = podSeriesDefs(ns, appPodRegex(ids))
	default:
		return observability.QualityReport{}, errProbeTarget
	}
	// 稳定顺序（map 迭代随机，前端展示可预期）。
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	report := observability.QualityReport{
		Source:  observability.QualitySourceProm,
		Metrics: make([]observability.MetricQuality, 0, len(names)),
	}
	for _, mn := range names {
		q := observability.MetricQuality{Metric: mn, CheckedAt: time.Now()}
		v := url.Values{}
		v.Set("query", defs[mn].promQL)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query?"+v.Encode(), nil)
		if err != nil {
			q.Status = observability.QualityFailed
			q.Error = "构造查询失败"
			report.Metrics = append(report.Metrics, q)
			continue
		}
		resp, err := fetchJSON[promInstantResponse](s.client, req)
		if err != nil {
			// err 含上游 URL/连接细节（dial tcp http://prom:9090）——脱敏只回报分类。
			log.Printf("observability quality probe %s/%s metric=%s: 调 Prometheus 失败: %v", targetType, targetID, mn, err)
			q.Status = observability.QualityFailed
			q.Error = "连接采集源失败（Prometheus 不可达或超时）"
			report.Metrics = append(report.Metrics, q)
			continue
		}
		if resp.Status != "success" {
			q.Status = observability.QualityFailed
			q.Error = "采集源返回错误：" + resp.Error
			report.Metrics = append(report.Metrics, q)
			continue
		}
		if len(resp.Data.Result) == 0 {
			// 查询成功无数据：时间范围/目标/埋点检查（如 RED 依赖应用暴露 /metrics）。
			q.Status = observability.QualityEmpty
		} else {
			q.Status = observability.QualityOK
			q.SampleSeries = len(resp.Data.Result)
		}
		report.Metrics = append(report.Metrics, q)
	}
	return report, nil
}
