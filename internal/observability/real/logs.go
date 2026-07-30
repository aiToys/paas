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

// LogsStore 调 Loki HTTP API 实现 LogsReader。
type LogsStore struct {
	lokiURL string
	client  *http.Client
}

// NewLogsStore 创建 Loki 适配。lokiURL 为 Loki 根地址（如 http://loki:3100）。
func NewLogsStore(lokiURL string) *LogsStore {
	return &LogsStore{lokiURL: lokiURL, client: &http.Client{Timeout: 10 * time.Second}}
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

// ListLogs 调 Loki 查最近 1h 日志，按 appID/level/q 过滤（LogQL selector + |=）。
// Loki 已按时间倒序返回；截断 limit。后端不可达降级返空。
func (s *LogsStore) ListLogs(ctx context.Context, appID, level, q string, limit int) ([]observability.LogEntry, error) {
	if limit <= 0 || limit > observability.MaxLogs {
		limit = 100
	}
	// LogQL：{app="...",level="..."} |= "关键字"
	selector := "{"
	if appID != "" {
		selector += fmt.Sprintf("app=%q,", appID)
	}
	if level != "" {
		selector += fmt.Sprintf("level=%q,", level)
	}
	selector += "}"
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
		for _, val := range r.Values {
			if len(val) < 2 {
				continue
			}
			tsNs, _ := strconv.ParseInt(val[0], 10, 64)
			out = append(out, observability.LogEntry{
				ID:        fmt.Sprintf("%s/%s", r.Stream["app"], val[0]),
				AppID:     r.Stream["app"],
				Level:     r.Stream["level"],
				Message:   val[1],
				Timestamp: time.Unix(0, tsNs),
			})
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
