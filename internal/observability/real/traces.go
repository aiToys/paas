package real

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// TracesStore 调 Jaeger HTTP API（v1）实现 TracesReader。
//
// 选 Jaeger 而非 Tempo：Jaeger all-in-one 是天生单体（Go，~256Mi 稳定跑），Tempo single-binary
// 把 ingester+querier+compactor 塞一个进程，512Mi OOM、2Gi 才稳。CNCF 毕业，生态最成熟。
// core 推送端零改动（OTLP/HTTP 4318，Jaeger 原生接收）；查询端适配 v1 原生 JSON model。
type TracesStore struct {
	jaegerURL string
	client    *http.Client
	lister    observability.AppWorkloadLister    // 应用级查询：app→工作负载名（service 过滤）
	entities  observability.TenantEntityLister   // 租户服务白名单：平台级查询 + GetTrace 归属校验
	cache     *queryCache[[]observability.Trace] // singleflight + 短 TTL（R8-I1 轮询 QPS 削峰）
}

// NewTracesStore 创建 Jaeger 适配。jaegerURL 为 Jaeger Query 根地址（如 http://jaeger:16686）。
// lister 可为 nil（应用级查询降级返空）。entities 为 nil 时平台级查询与 GetTrace 归属校验
// 降级返空（fail-closed，与租户隔离语义一致——宁可无数据不可跨租户泄漏）。
func NewTracesStore(jaegerURL string, lister observability.AppWorkloadLister, entities observability.TenantEntityLister) *TracesStore {
	return &TracesStore{jaegerURL: jaegerURL, client: httputil.NewClient(10 * time.Second), lister: lister, entities: entities, cache: newQueryCache[[]observability.Trace](5 * time.Second)}
}

// Jaeger v1 /api/traces 响应结构（最小子集）。
// 一条 trace = spans 平铺列表 + processes 字典（span.processID → service 元信息）。
// list 接口一次返完整 span 树（含 processes/tags/references），无需二次拉详情。
type jaegerTraceResponse struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	TraceID   string                `json:"traceID"`
	Spans     []jaegerSpan          `json:"spans"`
	Processes map[string]jaegerProc `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	References    []jaegerRef `json:"references"`
	StartTime     int64       `json:"startTime"` // 微秒（Unix microseconds）
	Duration      int64       `json:"duration"`  // 微秒
	Tags          []jaegerTag `json:"tags"`
	ProcessID     string      `json:"processID"`
}

type jaegerRef struct {
	RefType string `json:"refType"` // CHILD_OF | FOLLOWS_FROM
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerProc struct {
	ServiceName string      `json:"serviceName"`
	Tags        []jaegerTag `json:"tags"`
}

// jaegerTag 的 value 类型不定（string/int64/float64/bool/binary），用 RawMessage 原始保留后按 type/内容解析。
type jaegerTag struct {
	Key   string          `json:"key"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// GetTrace 按 traceID 精确查单条（Jaeger /api/traces/{traceID}）。
// 排障直查入口：日志/告警/响应头拿到 traceId 后直接定位完整链路。不存在返 ErrTraceNotFound。
//
// 租户归属校验（fail-closed）：解析后校验 trace 的 service.name ∈ 本租户服务白名单
// （TenantServiceNames + 控制面自身），跨租户/未注入 entities 统一 ErrTraceNotFound 不泄漏存在性。
// traceID 未转义直拼 URL 会路径穿越，先正则校验 hex 格式。
func (s *TracesStore) GetTrace(ctx context.Context, traceID string) (observability.Trace, error) {
	if !validTraceID(traceID) {
		return observability.Trace{}, observability.ErrTraceNotFound
	}
	allow, err := s.tenantServices(ctx)
	if err != nil {
		return observability.Trace{}, err
	}
	if allow == nil {
		return observability.Trace{}, observability.ErrTraceNotFound // fail-closed：无法确定归属即拒
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jaegerURL+"/api/traces/"+traceID, nil)
	if err != nil {
		return observability.Trace{}, err
	}
	resp, err := fetchJSON[jaegerTraceResponse](s.client, req)
	if err != nil {
		return observability.Trace{}, err
	}
	if len(resp.Data) == 0 {
		return observability.Trace{}, observability.ErrTraceNotFound
	}
	tr, hasErr := parseJaegerTrace(resp.Data[0])
	if hasErr {
		tr.Status = observability.TraceError
	}
	// 归属校验：任一 span 的 service.name 在租户白名单内即视为本租户 trace
	//（跨服务调用 trace 可能含他人 client span，但入口在本租户即归属本租户）。
	ok := false
	for _, sp := range tr.Spans {
		if _, hit := allow[sp.Service]; hit {
			ok = true
			break
		}
	}
	if !ok {
		return observability.Trace{}, observability.ErrTraceNotFound
	}
	return tr, nil
}

// ListTraces 调 Jaeger /api/traces 查最近 1h trace，list 接口已返完整 span 树，直接解析填充 Spans。
//
// 应用级（appID 非空）：该应用的 service 工作负载名 = OTel span 的 service.name（约定 Deployment/Service
// 名 = 工作负载名）。经 AppWorkloadLister 解析 app→工作负载名列表，逐个按 service 查合并去重。
// lister 未注入 / app 无 service 工作负载 → 降级返空。
//
// 平台级（appID 空）：不带 service 查全部（控制面 paas-core + 全部应用服务）。
//
// 多租户隔离：service.name 在租户 namespace 内唯一但跨租户可能重名，属诚实限制（与 metrics/logs
// 按 pod 正则聚合的同款取舍）；彻底隔离需应用 Pod 注入 tenant 资源属性（留后续）。
// 后端不可达降级返空（不报错）。
func (s *TracesStore) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	// 无租户 ctx（测试/内部调用）跳过缓存直查，保持原降级语义。
	tid, tidErr := tenant.IDOrErr(ctx)
	if tidErr != nil {
		return s.listTraces(ctx, appID, status, limit)
	}
	key := "t:" + tid + ":" + appID + ":" + status + ":" + observability.RangeFrom(ctx).String()
	return s.cache.do(ctx, key, func(c context.Context) ([]observability.Trace, error) {
		return s.listTraces(c, appID, status, limit)
	})
}

func (s *TracesStore) listTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	if limit <= 0 || limit > observability.MaxTraces {
		limit = 50
	}
	// Jaeger start/end 用微秒（Unix microseconds）。时间范围选择器（默认 1h）。
	now := time.Now()
	window := observability.RangeFrom(ctx)
	start := strconv.FormatInt(now.Add(-window).UnixMicro(), 10)
	end := strconv.FormatInt(now.UnixMicro(), 10)

	var merged []observability.Trace
	if appID != "" {
		if s.lister == nil {
			return []observability.Trace{}, nil
		}
		names, err := s.lister.AppWorkloadNames(ctx, appID)
		if err != nil {
			log.Printf("observability real traces: 解析应用工作负载名失败 app=%s: %v", appID, err)
			return []observability.Trace{}, nil
		}
		if len(names) == 0 {
			return []observability.Trace{}, nil
		}
		merged = make([]observability.Trace, 0, limit)
		seen := make(map[string]struct{}, limit)
		// 每个工作负载查 limit 条（合并后整体截断）；应用工作负载数有限（通常个位数）。
		for _, name := range names {
			for _, trc := range s.query(ctx, name, start, end, limit) {
				if _, dup := seen[trc.ID]; dup {
					continue
				}
				seen[trc.ID] = struct{}{}
				trc.AppID = appID
				merged = append(merged, trc)
			}
		}
	} else {
		// 平台级（租户全局视图）：以租户服务白名单为查询范围（TenantServiceNames），
		// 禁止 Jaeger /api/services 全量枚举（会跨租户泄漏其它租户服务名与 trace）。
		// 未注入 entities 时 fail-closed 返空。
		allow, err := s.tenantServices(ctx)
		if err != nil || allow == nil {
			return []observability.Trace{}, nil
		}
		merged = s.queryServices(ctx, s.allowedServiceNames(allow), start, end, limit)
	}

	// 噪音降权：单 span 且 0ms 的探活类 trace（如 mcp /mcp 健康检查）一次一批占满列表，
	// 掩盖真实业务链路（dogfooding 实测 8 条探活刷屏）。不丢弃（直查 TraceID 仍可见），
	// 仅在默认列表中排除——保留 >=2 span 或耗时 >0 的真实链路。
	if len(merged) > 0 {
		signal := merged[:0]
		for _, t := range merged {
			if len(t.Spans) <= 1 && t.DurationMs <= 0 {
				continue
			}
			signal = append(signal, t)
		}
		merged = signal
	}

	// status 过滤先于截断（与 memory 路径语义一致：先过滤后取 limit，
	// 防「前 limit 条多为 success」时 error 过滤后远少于应有数量）。
	if status != "" {
		filtered := merged[:0]
		for _, t := range merged {
			if t.Status == status {
				filtered = append(filtered, t)
			}
		}
		merged = filtered
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].StartedAt.After(merged[j].StartedAt) })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// queryAllServices 平台级查询：按给定服务名列表逐个查 limit 条合并去重。
func (s *TracesStore) queryServices(ctx context.Context, services []string, start, end string, limit int) []observability.Trace {
	out := make([]observability.Trace, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, svc := range services {
		if svc == "jaeger-all-in-one" {
			continue // Jaeger 自身服务，非业务 trace
		}
		for _, trc := range s.query(ctx, svc, start, end, limit) {
			if _, dup := seen[trc.ID]; dup {
				continue
			}
			seen[trc.ID] = struct{}{}
			out = append(out, trc)
		}
	}
	return out
}

// listServices 调 Jaeger /api/services 拿全部服务名（业务 + 平台）。失败降级返空。
func (s *TracesStore) listServices(ctx context.Context) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jaegerURL+"/api/services", nil)
	if err != nil {
		log.Printf("observability real traces: 构造 /api/services 请求失败: %v", err)
		return nil
	}
	resp, err := fetchJSON[struct {
		Data []string `json:"data"`
	}](s.client, req)
	if err != nil {
		log.Printf("observability real traces: 调 Jaeger /api/services 失败: %v", err)
		return nil
	}
	return resp.Data
}

// tenantServices 返回本租户可见的服务名集合（含控制面 paas-core）。
// 未注入 entities 或列举失败返 nil（fail-closed——调用方据此拒查/返空，不跨租户泄漏）。
func (s *TracesStore) tenantServices(ctx context.Context) (map[string]struct{}, error) {
	if s.entities == nil {
		return nil, nil
	}
	names, err := s.entities.TenantServiceNames(ctx)
	if err != nil {
		log.Printf("observability real traces: 枚举租户服务失败: %v", err)
		return nil, err
	}
	allow := make(map[string]struct{}, len(names)+1)
	for _, n := range names {
		allow[n] = struct{}{}
	}
	allow["paas-core"] = struct{}{} // 控制面自身 trace 对租户可见（排障入口）
	return allow, nil
}

// allowedServiceNames 从集合取有序切片（排序保证查询顺序确定性）。
func (s *TracesStore) allowedServiceNames(allow map[string]struct{}) []string {
	names := make([]string, 0, len(allow))
	for n := range allow {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// validTraceID 校验 traceID 为 16-32 位 hex（Jaeger/W3C 格式），防路径穿越与 query 注入。
func validTraceID(id string) bool {
	if len(id) < 16 || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// query 调 Jaeger /api/traces，service 空则不限服务。响应一次含完整 span 树，直接 parse。
// 失败降级返空切片（不报错，与 Tempo 旧实现同款）。
func (s *TracesStore) query(ctx context.Context, service, start, end string, limit int) []observability.Trace {
	v := url.Values{}
	if service != "" {
		v.Set("service", service)
	}
	v.Set("limit", strconv.Itoa(limit))
	v.Set("start", start)
	v.Set("end", end)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jaegerURL+"/api/traces?"+v.Encode(), nil)
	if err != nil {
		log.Printf("observability real traces: 构造请求失败: %v", err)
		return nil
	}
	resp, err := fetchJSON[jaegerTraceResponse](s.client, req)
	if err != nil {
		log.Printf("observability real traces: 调 Jaeger 失败 service=%s: %v", service, err)
		return nil
	}
	out := make([]observability.Trace, 0, len(resp.Data))
	for _, jt := range resp.Data {
		tr, hasErr := parseJaegerTrace(jt)
		if hasErr {
			tr.Status = observability.TraceError
		}
		out = append(out, tr)
	}
	return out
}

// parseJaegerTrace 把 Jaeger v1 trace 解析为领域 Trace。
//
// trace 无顶层时间/时长字段，从 spans 计算（min(startTime) 为 trace 起点，max(end)-min(start) 为总时长）。
// 根 span = 无 CHILD_OF/FOLLOWS_FROM 引用且 startTime 最早的 span，其 operationName/serviceName 作 trace 入口信息。
//
// 错误判定：span 含 error=true tag（OTLP status ERROR 经 collector 转 Jaeger error tag），
// 或 http.response.status_code>=500 → IsError=true。
// 异常信息：exception.type / exception.message（otelhttp 5xx/panic 自动打）。
// Tags = span tags 全量透传（http.*/client.address/exception.*/自定义任意属性）。
func parseJaegerTrace(jt jaegerTrace) (observability.Trace, bool) {
	tr := observability.Trace{ID: jt.TraceID, Status: observability.TraceSuccess}
	if len(jt.Spans) == 0 {
		return tr, false
	}
	var minStart int64 = -1
	var maxEnd int64
	for _, sp := range jt.Spans {
		end := sp.StartTime + sp.Duration
		if minStart < 0 || sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		if end > maxEnd {
			maxEnd = end
		}
	}

	spans := make([]observability.Span, 0, len(jt.Spans))
	hasError := false
	var rootOp, rootSvc string
	rootStart := int64(-1)
	for _, sp := range jt.Spans {
		svc := jt.Processes[sp.ProcessID].ServiceName
		parent := firstParentRef(sp.References)
		tags := jaegerTagsToMap(sp.Tags)
		isErr := jaegerSpanIsError(sp.Tags)
		if isErr {
			hasError = true
		}
		var startMs, durMs int64
		if minStart > 0 {
			startMs = (sp.StartTime - minStart) / 1000 // us → ms（相对 trace 起点偏移）
		}
		durMs = sp.Duration / 1000
		// span.kind（server=入口/client=出站）+ client span 的真实对端。
		// 区分「谁发起调用、谁被调用」：client span 的 service.name 是调用方，
		// 真实下游取 peer.service > db.system > server.address（redis/db 等中间件无 peer.service）。
		kind := tagStr(sp.Tags, "span.kind")
		peer := tagStr(sp.Tags, "peer.service")
		if peer == "" {
			if db := tagStr(sp.Tags, "db.system"); db != "" {
				peer = db
			} else {
				peer = tagStr(sp.Tags, "server.address")
			}
		}
		spans = append(spans, observability.Span{
			ID:           sp.SpanID,
			ParentID:     parent,
			Operation:    sp.OperationName,
			Service:      svc,
			Kind:         kind,
			Peer:         peer,
			StartMs:      startMs,
			DurationMs:   durMs,
			IsError:      isErr,
			ErrorType:    tagStr(sp.Tags, "exception.type"),
			ErrorMessage: tagStr(sp.Tags, "exception.message"),
			Tags:         tags,
		})
		// 根 span：无 parent 且 startTime 最早（多根时取最早，确定性）。
		if parent == "" && (rootStart < 0 || sp.StartTime < rootStart) {
			rootStart = sp.StartTime
			rootOp = sp.OperationName
			rootSvc = svc
		}
	}
	tr.Operation = rootOp
	tr.Service = rootSvc
	tr.Spans = spans
	tr.StartedAt = time.UnixMicro(minStart)
	tr.DurationMs = (maxEnd - minStart) / 1000
	return tr, hasError
}

// firstParentRef 返回首个 CHILD_OF/FOLLOWS_FROM 引用的 spanID（根 span 返空）。
func firstParentRef(refs []jaegerRef) string {
	for _, r := range refs {
		if r.RefType == "CHILD_OF" || r.RefType == "FOLLOWS_FROM" {
			return r.SpanID
		}
	}
	return ""
}

// jaegerSpanIsError 判 span 是否出错：error tag=true（OTLP status ERROR 映射）或 HTTP 5xx。
// HTTP 状态码同时检查新旧 semconv key（>=v1.26 http.response.status_code / 旧版 http.status_code，
// 大量第三方 instrument 库仍在用旧 key）。
func jaegerSpanIsError(tags []jaegerTag) bool {
	for _, t := range tags {
		if t.Key == "error" && tagBool(t) {
			return true
		}
	}
	for _, key := range []string{"http.response.status_code", "http.status_code"} {
		if code := tagStr(tags, key); code != "" {
			if n, err := strconv.Atoi(code); err == nil && n >= 500 {
				return true
			}
		}
	}
	return false
}

// jaegerTagsToMap 全量透传 span tags 为 key→string map（前端按属性表展示）。
// bool/double/int 统一转字符串；空数组返 nil。
func jaegerTagsToMap(tags []jaegerTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		v := tagStrByKey(tags, t.Key)
		if v == "" {
			continue
		}
		m[t.Key] = v
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// tagStr 按 key 取 tag 字符串值（任意类型转 string）。
func tagStr(tags []jaegerTag, key string) string {
	return tagStrByKey(tags, key)
}

// tagStrByKey 从 tags 取首个匹配 key 的字符串值。
// Jaeger value 可能是 JSON string（"GET"）/number（200）/bool（true），先按 string 解，
// 失败则原样 trim（number/bool 的 RawMessage 是裸字面量 200/true）。
func tagStrByKey(tags []jaegerTag, key string) string {
	for _, t := range tags {
		if t.Key != key {
			continue
		}
		if len(t.Value) == 0 {
			return ""
		}
		var s string
		if json.Unmarshal(t.Value, &s) == nil {
			return s
		}
		// number/bool：去掉 JSON 字面量外层空白（无引号），如 200 / true / 1.5。
		return strings.TrimSpace(string(t.Value))
	}
	return ""
}

// tagBool 解析 bool tag（error=true）。
func tagBool(t jaegerTag) bool {
	var b bool
	if json.Unmarshal(t.Value, &b) == nil {
		return b
	}
	// 兜底：type 字符串形如 "true"。
	return strings.EqualFold(strings.TrimSpace(string(t.Value)), "true")
}
