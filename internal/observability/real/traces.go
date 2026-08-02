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
	"github.com/aitoys/paas/pkg/tenant"
)

// TracesStore 调 Tempo HTTP API 实现 TracesReader。
type TracesStore struct {
	tempoURL string
	client   *http.Client
}

// NewTracesStore 创建 Tempo 适配。tempoURL 为 Tempo 根地址（如 http://tempo:3200）。
func NewTracesStore(tempoURL string) *TracesStore {
	return &TracesStore{tempoURL: tempoURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// tempoSearchResponse 是 Tempo /api/search 响应的最小子集。
type tempoSearchResponse struct {
	Traces []struct {
		TraceID         string  `json:"traceID"`
		RootServiceName string  `json:"rootServiceName"`
		RootTraceName   string  `json:"rootTraceName"`
		DurationSeconds float64 `json:"durationSeconds"`
		StartTimeUnixNs uint64  `json:"startTimeUnixNs"`
	} `json:"traces"`
}

// ListTraces 调 Tempo search 查最近 1h trace，按 appID（tags=app=...）过滤。
// 注：span 详情（OTLP /api/traces/{id}）解析复杂，本期 search 返回基本信息 + 空 Spans，留后续。
// 后端不可达降级返空。
func (s *TracesStore) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	if limit <= 0 || limit > observability.MaxTraces {
		limit = 50
	}
	v := url.Values{}
	v.Set("limit", strconv.Itoa(limit))
	// Tempo tag 过滤：app=<appID> + tenant=<tid>（多租户隔离）。
	// 注：应用 span 需 OTel 资源属性带 app/tenant，否则查不到（应用埋点留后续 P2）。
	tags := ""
	if appID != "" {
		tags = fmt.Sprintf("app=%s", appID)
	}
	if tid, ok := tenant.TenantFrom(ctx); ok && tid != "" {
		if tags != "" {
			tags += " "
		}
		tags += fmt.Sprintf("tenant=%s", tid)
	}
	if tags != "" {
		v.Set("tags", tags)
	}
	now := time.Now()
	v.Set("start", strconv.FormatInt(now.Add(-time.Hour).Unix(), 10))
	v.Set("end", strconv.FormatInt(now.Unix(), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tempoURL+"/api/search?"+v.Encode(), nil)
	if err != nil {
		log.Printf("observability real traces: 构造请求失败: %v", err)
		return []observability.Trace{}, nil
	}
	tr, err := fetchJSON[tempoSearchResponse](s.client, req)
	if err != nil {
		log.Printf("observability real traces: 调 Tempo 失败: %v", err)
		return []observability.Trace{}, nil
	}
	out := make([]observability.Trace, 0, len(tr.Traces))
	for _, t := range tr.Traces {
		// StartTimeUnixNs 为 uint64，截断为 int64 时超 int63 会得到负时间戳；
		// 这里取低 63 位派生确定性时间（trace 已按时间排序，绝对精度非关键）。
		ns := int64(t.StartTimeUnixNs % (1 << 63)) //nolint:gosec // G115: 已用 %(1<<63) 截断防 uint64->int64 overflow
		trc := observability.Trace{
			ID:         t.TraceID,
			AppID:      appID,
			Operation:  t.RootTraceName,
			Status:     observability.TraceSuccess,
			DurationMs: int64(t.DurationSeconds * 1000),
			StartedAt:  time.Unix(0, ns),
		}
		// status 过滤（Tempo search 无原生 status；本期客户端过滤，error trace 归后续 span tag 解析）。
		if status != "" && trc.Status != status {
			continue
		}
		out = append(out, trc)
	}
	return out, nil
}
