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
}

// NewLogsStore 创建 Loki 适配。lokiURL 为 Loki 根地址（如 http://loki:3100）。
func NewLogsStore(lokiURL string) *LogsStore {
	return &LogsStore{lokiURL: lokiURL, client: httputil.NewClient(10 * time.Second)}
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
// 应用级映射：workload_controller 给工作负载 Pod 打 label `paas.aitoys/app=<appID>`，
// promtail 采集时把 label key 的 `.`/`/` 转成 `_`，故 Loki stream label = `paas_aitoys_app`。
// LogQL selector 用该 label 精确匹配 appID；空 appID 用 `=~".+"` 匹配全部 PaaS 应用 Pod。
//
// level：Pod stdout 日志无结构化 level label，改用内容正则 best-effort 过滤
// （`|~ "(?i)\berror\b"` 等），非严格——仅缩小范围，不保证命中所有该级别日志。
//
// Loki 按时间倒序返回；截断 limit。后端不可达降级返空。
func (s *LogsStore) ListLogs(ctx context.Context, appID, level, q string, limit int) ([]observability.LogEntry, error) {
	if limit <= 0 || limit > observability.MaxLogs {
		limit = 100
	}
	// LogQL selector：按 paas_aitoys_app label 过滤
	appSel := fmt.Sprintf("paas_aitoys_app=%q", appID)
	if appID == "" {
		appSel = `paas_aitoys_app=~".+"` // 全部 PaaS 应用 Pod（带 app label）
	}
	// 多租户隔离：限定本租户 Pod（paas_aitoys/tenant label 被 promtail 转下划线）。
	// 无 tenant ctx（测试/未鉴权）保持原查询，不额外过滤（兼容现有行为）。
	selector := "{" + appSel
	if tid, ok := tenant.TenantFrom(ctx); ok && tid != "" {
		selector += fmt.Sprintf(",paas_aitoys_tenant=%q", tid)
	}
	selector += "}"
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
		app := r.Stream["paas_aitoys_app"]
		pod := r.Stream["pod"]
		for _, val := range r.Values {
			if len(val) < 2 {
				continue
			}
			tsNs, _ := strconv.ParseInt(val[0], 10, 64)
			msg := val[1]
			out = append(out, observability.LogEntry{
				ID:        fmt.Sprintf("%s/%s", pod, val[0]),
				AppID:     app,
				Level:     inferLevel(msg),
				Message:   msg,
				TraceID:   r.Stream["trace_id"],
				Timestamp: time.Unix(0, tsNs),
			})
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
