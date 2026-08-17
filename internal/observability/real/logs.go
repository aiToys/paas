package real

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// LogsStore 调 Loki HTTP API 实现 LogsReader。
type LogsStore struct {
	lokiURL string
	client  *http.Client
	lister  observability.AppWorkloadLister // 应用级查询：解析 app→工作负载 ID（pod 名正则）
}

// NewLogsStore 创建 Loki 适配。lokiURL 为 Loki 根地址（如 http://loki:3100）。
// lister 可为 nil（应用级查询降级返空）。
func NewLogsStore(lokiURL string, lister observability.AppWorkloadLister) *LogsStore {
	return &LogsStore{lokiURL: lokiURL, client: httputil.NewClient(10 * time.Second), lister: lister}
}

// lokiResponse 是 Loki /loki/api/v1/query_range 响应的最小子集。
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [tsNs, "line"]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// ListLogs 调 Loki 查最近 1h 日志，按 appID/level/q 过滤。
//
// 应用级查询：promtail 默认只提取 app/namespace 等通用 stream label，不含自定义 `paas.aitoys/app`，
// 故按 namespace(paas-<tenant>) + 工作负载 pod 名正则（wl-<id>-.*）匹配，与 metrics 同源。
// 空 appID 用 namespace 内 `pod=~"wl-.*"` 匹配该租户全部 PaaS 应用 Pod（带 tenant 隔离）。
//
// level：Pod stdout 日志无结构化 level label，改用内容正则 best-effort 过滤
// （`|~ "(?i)\berror\b"` 等），非严格——仅缩小范围，不保证命中所有该级别日志。
//
// Loki 按时间倒序返回；截断 limit。后端不可达 / lister 未注入降级返空。
func (s *LogsStore) ListLogs(ctx context.Context, appID, targetType, targetID, level, q, lane string, limit int) ([]observability.LogEntry, error) {
	if limit <= 0 || limit > observability.MaxLogs {
		limit = 100
	}
	tid, _ := tenant.TenantFrom(ctx)
	ns := tenant.Namespace(tid) // paas-<tenant>，多租户隔离（空 tid 兜底 paas-x）
	// Pod 名正则：按 targetType 选维度。
	//   - dataservice：数据服务 STS Pod 名 = <ds-id>-0（StatefulSet Pod，多副本 <ds-id>-N）
	//   - app：appID 指定时解析其工作负载 ID；否则匹配全部 wl-.* Pod（本租户）。
	podRegex := "wl-.*"
	if targetType == observability.TargetDataservice {
		if targetID == "" {
			return []observability.LogEntry{}, nil // dataservice 需指定 targetID
		}
		podRegex = regexp.QuoteMeta(targetID) + "-\\d+"
	} else if appID != "" {
		if s.lister == nil {
			return []observability.LogEntry{}, nil // 应用级需 lister
		}
		ids, err := s.lister.AppWorkloadIDs(ctx, appID)
		if err != nil || len(ids) == 0 {
			return []observability.LogEntry{}, nil // app 无工作负载：降级返空
		}
		podRegex = lokiPodSelector(ids)
	}
	// LogQL selector：限定本租户 ns + 应用 pod 名正则。
	// lane 非空时追加 lane label 过滤（promtail 从 Pod label paas.aitoys/lane 提取；
	// 泳道排障：同服务多泳道并行时区分流量归属，default 基线 lane=default）。
	selector := fmt.Sprintf(`{namespace=%q,pod=~%q`, ns, podRegex)
	if lane != "" {
		selector += fmt.Sprintf(`,lane=%q`, lane)
	}
	selector += `}`
	if level != "" {
		// 内容正则 best-effort（非严格 level label）
		selector += fmt.Sprintf(" |~ %q", "(?i)\\b"+regexp.QuoteMeta(level)+"\\b")
	}
	if q != "" {
		selector += fmt.Sprintf(" |= %q", q)
	}
	now := time.Now()
	v := url.Values{}
	v.Set("query", selector)
	v.Set("start", strconv.FormatInt(now.Add(-time.Hour).UnixNano(), 10))
	v.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	v.Set("limit", strconv.Itoa(limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lokiURL+"/loki/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		log.Printf("observability real logs: 构造请求失败: %v", err)
		return []observability.LogEntry{}, nil
	}
	lr, err := fetchJSON[lokiResponse](s.client, req)
	if err != nil {
		log.Printf("observability real logs: 调 Loki 失败: %v", err)
		return []observability.LogEntry{}, nil
	}
	if lr.Status != "success" {
		log.Printf("observability real logs: Loki 返回非 success: %s", lr.Error)
		return []observability.LogEntry{}, nil
	}
	out := make([]observability.LogEntry, 0, limit)
	for _, r := range lr.Data.Result {
		pod := r.Stream["pod"]
		for _, val := range r.Values {
			if len(val) < 2 {
				continue
			}
			tsNs, _ := strconv.ParseInt(val[0], 10, 64)
			msg := val[1]
			le := observability.LogEntry{
				ID:        fmt.Sprintf("%s/%s", pod, val[0]),
				Level:     inferLevel(msg),
				Message:   msg,
				TraceID:   r.Stream["trace_id"],
				Timestamp: time.Unix(0, tsNs),
			}
			// 维度归属：dataservice 填 TargetType/TargetID；app 填 AppID
			// （stream 已按 ns+pod 正则限定，无 paas_aitoys_app label，故维度来自查询参数）。
			if targetType == observability.TargetDataservice {
				le.TargetType = observability.TargetDataservice
				le.TargetID = targetID
			} else {
				le.AppID = appID
			}
			out = append(out, le)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// inferLevel 从日志内容 best-effort 推断级别（Pod stdout 无结构化 level label）。
// 匹配常见错误/告警关键字；否则默认 info。
func inferLevel(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "panic"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	}
	return "info"
}
