# 可观测依赖资源增强 + 引擎 exporter + 深链接 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让应用详情→可观测→依赖资源 tab 成为可排障的排障单元（引擎业务指标 + Pod + 日志 + 告警 + 下钻），并让全系统页面分享 URL 能定位到分享前位置（tab + 筛选 + 列表筛选 + 环境上下文）。

**Architecture:**
- 后端：dataservice STS 按 engine 注入 exporter sidecar（复用 Pod label 天然多租户隔离）；real/metrics.go 加 PVC 用量（kubelet_volume_stats）+ 按 kind/engine 业务指标 PromQL；新增 `GET /api/dataservices/{id}/pods`；logs/alerts 加 targetType/targetId 过滤。
- 前端：抽 `useUrlState` composable 统一状态↔URL query；envStore env 进 URL；AppObservability 依赖资源 tab 重构（按 kind 动态选指标 + Pod + 告警 + 日志 + 下钻）；DataServiceDetail 卡片错配修复。

**Tech Stack:** Go（controller-runtime k8s.io/api）、Vue 3 + Element Plus + Pinia + vue-router 4、Prometheus/Loki（已有真实后端）。

## Global Constraints（逐条摘自 spec，所有任务隐含遵守）

- **诚实降级**：所有新增指标/日志/Pod 在 exporter 未就绪或 real 查不到时静默返空，前端显示「-」，**不造伪指标**。real 模式 Prometheus/Loki 调失败 `log.Printf` + 返空切片，不 5xx、不 panic。
- **多租户隔离**：sidecar 同 Pod 继承 `paas.aitoys/tenant` label；Pod 端点/logs/alerts 全部按 ctx tenant 过滤（namespace=`paas-<tenant>`）；跨租户统一 not found 不泄漏。
- **镜像内网化**：exporter 镜像统一 `PAAS_IMAGE_REGISTRY`（`<registry>/library/<name>:<tag>`，**amd64**，经 daocloud 中转 `docker.m.daocloud.io`），与引擎主镜像同路径；`registry==""` 时用 docker.io 原值（与 `engineImage` 同款）。
- **best-effort 不阻断**：sidecar 注入失败仅 log，不阻断主容器/STS 创建；pods 端点 reader nil 时返空（集群外降级，与 Restarter 同款 typed-nil 防御）。
- **响应契约**：所有新端点成功 `{data:T}`（`httputil.WriteData`），错误 `{error:msg}`（`WriteServiceError`），500 脱敏（`WriteInternalError`）。
- **不执行 git commit / 分支操作**（除非用户明确要求）。
- **Go 注释语言**：中文，与代码库现有注释一致。

## File Structure

**后端（Go）：**
- `internal/observability/model.go` — 加引擎业务指标常量（`MetricConnections/MetricQPS/MetricHitRate/MetricLag/MetricVectors/MetricDiskUsage`）+ `LogEntry` 加 `TargetType/TargetID` 字段。
- `internal/observability/readers.go` — `LogsReader.ListLogs` 签名加 `targetType, targetID` 参数。
- `internal/observability/real/logs.go` — 按 targetType 选 pod 正则（dataservice=`ds-<id>-0`，app=原逻辑）。
- `internal/observability/memory/store.go` — `ListLogs` 加 targetType 参数路由。
- `internal/observability/compose/compose.go` — `ListLogs`/`ListAlerts` 透传新参数；`ListAlerts` 按 targetType/targetId 过滤。
- `internal/observability/handler.go` — logs/alerts handler 读 `targetType/targetId` query。
- `internal/observability/real/metrics.go` — dataservice 分支 `defs` 加 disk_usage + 按 kind/engine 业务指标。
- `internal/dataservice/handler.go` — serveItem 加 `pods` 分支 + `PodReader` 接口 + `WithPodReader`。
- `internal/controller/dataservice_controller.go` — `exporterSidecar(d)` sidecar 注入；扩 PVC 用量查询可选。
- `internal/controller/dataservice_status.go`（**新建**）— `K8sPodReader` 实现 dataservice.PodReader（查 label `paas.aitoys/dataservice=<id>` Pod）。
- `cmd/core/main.go` — 桥接 PodReader 注入两路径 handler；observability logs targetType 透传。
- `internal/observability/*_test.go` + `internal/dataservice/*_test.go` + `internal/controller/*_test.go` — 测试。

**前端（TS/Vue）：**
- `frontend/console-user/src/composables/useUrlState.ts`（**新建**）— 双向同步状态↔URL query。
- `frontend/console-user/src/stores/env.ts` — switchEnv 写 `env` query；初始化从 query 恢复。
- `frontend/console-user/src/views/App.vue` — 启动从 `route.query.env` 恢复 envStore。
- `frontend/console-user/src/views/app-tabs/AppObservability.vue` — 依赖资源 tab 重构（动态指标 + Pod + 告警 + 日志 + 下钻）。
- `frontend/console-user/src/views/DataServiceDetail.vue` — metricOrder 按 kind 动态选（移除 dataservice 无意义 rps/latency）。
- `frontend/console-user/src/views/Applications.vue` + `Workloads.vue` + `DataServices.vue` — 搜索/筛选接 useUrlState。

---

## Task 1: 加引擎业务指标 + disk_usage 领域常量

**Files:**
- Modify: `internal/observability/model.go:18-29`（指标名常量段）

**Interfaces:**
- Produces: `observability.MetricConnections = "connections"`、`MetricQPS = "qps"`、`MetricHitRate = "hit_rate"`、`MetricLag = "lag"`、`MetricVectors = "vectors"`、`MetricDiskUsage = "disk_usage"` 常量，供 Task 5（real/metrics PromQL）+ 前端按 kind 选指标消费。

- [ ] **Step 1: 加常量**

在 `internal/observability/model.go` 的指标名常量块（`MetricErrorRate` 后）追加：

```go
// 引擎业务指标（数据服务排障：需引擎 exporter sidecar 提供，见 controller.exporterSidecar）。
const (
	MetricConnections = "connections" // 数据库/缓存/MQ 连接数
	MetricQPS         = "qps"         // 查询/事务每秒数（db/cache/vector/search）
	MetricHitRate     = "hit_rate"    // 缓存命中率 %（redis）
	MetricLag         = "lag"         // 消息堆积（MQ pending）
	MetricVectors     = "vectors"     // 向量数（qdrant collection）
	MetricDiskUsage   = "disk_usage"  // PVC 磁盘使用率 %（kubelet_volume_stats，无需 exporter）
)
```

- [ ] **Step 2: 编译验证**

Run: `go build ./internal/observability/...`
Expected: 编译通过（新常量未使用不报错，Go 包级常量允许未用）。

- [ ] **Step 3: Commit**

```bash
git add internal/observability/model.go
git commit -m "feat(observability): 加引擎业务指标 + disk_usage 领域常量"
```

---

## Task 2: LogEntry 加 targetType/targetID 字段（支撑 dataservice 日志查询）

**Files:**
- Modify: `internal/observability/model.go`（`LogEntry` 结构体，grep 定位）
- Test: `internal/observability/memory/store_test.go`

**Interfaces:**
- Produces: `LogEntry.TargetType string` + `LogEntry.TargetID string`（`json:"targetType,omitempty"`/`json:"targetId,omitempty"`），供 Task 3/4 memory/real 按 dataservice 维度过滤/填充。
- Consumes: 无（纯字段追加）。

- [ ] **Step 1: 定位 LogEntry 定义**

Run: `grep -n "type LogEntry struct" internal/observability/model.go`

- [ ] **Step 2: 加字段**

在 `LogEntry` 结构体内加两个字段（`AppID` 后）：

```go
TargetType string `json:"targetType,omitempty"` // app | dataservice（日志归属维度，空=历史 app 日志）
TargetID   string `json:"targetId,omitempty"`   // app 时=appID；dataservice 时=数据服务 ID
```

- [ ] **Step 3: 编译**

Run: `go build ./internal/observability/...`
Expected: 通过。

- [ ] **Step 4: Commit**

```bash
git add internal/observability/model.go
git commit -m "feat(observability): LogEntry 加 targetType/targetID 字段"
```

---

## Task 3: LogsReader 接口 + compose/memory 透传 targetType/targetId

**Files:**
- Modify: `internal/observability/readers.go:14`（LogsReader 接口）
- Modify: `internal/observability/memory/store.go:194`（ListLogs 签名 + 路由）
- Modify: `internal/observability/compose/compose.go:32`（ListLogs 透传）
- Modify: `internal/observability/handler.go:94`（serveLogs 读 query 透传）
- Test: `internal/observability/memory/store_test.go`、`internal/observability/handler_test.go`

**Interfaces:**
- Produces: `LogsReader.ListLogs(ctx, appID, targetType, targetID, level, q string, limit int)` 新签名；handler 从 `?targetType=&targetId=` 读取。
- Consumes: Task 2 的 LogEntry.TargetType/TargetID。
- 注：签名加参数后所有实现（memory/real/compose）+ cmd/core 调用点同步改。

- [ ] **Step 1: 写失败测试（memory 按 targetType 路由）**

在 `internal/observability/memory/store_test.go` 追加：

```go
func TestListLogsDataserviceTarget(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 灌一条 dataservice 日志
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TargetType: "dataservice", TargetID: "ds-1", Level: "error", Message: "conn refused", Timestamp: time.Now()},
		{ID: "l2", AppID: "app-x", Level: "info", Message: "app log", Timestamp: time.Now()},
	}
	// dataservice 维度查：只命中 ds-1
	got, err := s.ListLogs(ctx, "", "dataservice", "ds-1", "", "", 10)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "l1" {
		t.Fatalf("期望只命中 ds-1 的 l1，实际 %+v", got)
	}
	// app 维度查（appID 非空，targetType 空）：只命中 app-x
	got2, _ := s.ListLogs(ctx, "app-x", "", "", "", "", 10)
	if len(got2) != 1 || got[2:][0] != nil && got2[0].ID != "l2" {
		// 修正断言
	}
	_ = got2
}
```

（测试中 `got2` 断言简化：`if len(got2) != 1 || got2[0].ID != "l2" { t.Fatalf(...) }`——去掉上面那行错误断言，直接写：）

实际写入测试（干净版）：

```go
func TestListLogsDataserviceTarget(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TargetType: "dataservice", TargetID: "ds-1", Level: "error", Message: "conn refused", Timestamp: time.Now()},
		{ID: "l2", AppID: "app-x", Level: "info", Message: "app log", Timestamp: time.Now()},
	}
	got, err := s.ListLogs(ctx, "", "dataservice", "ds-1", "", "", 10)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "l1" {
		t.Fatalf("dataservice 维度期望命中 l1，实际 %+v", got)
	}
	got2, _ := s.ListLogs(ctx, "app-x", "", "", "", "", 10)
	if len(got2) != 1 || got2[0].ID != "l2" {
		t.Fatalf("app 维度期望命中 l2，实际 %+v", got2)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/observability/memory/ -run TestListLogsDataserviceTarget -v`
Expected: 编译失败（ListLogs 签名不匹配）。

- [ ] **Step 3: 改 readers.go 接口签名**

```go
type LogsReader interface {
	ListLogs(ctx context.Context, appID, targetType, targetID, level, q string, limit int) ([]LogEntry, error)
}
```

- [ ] **Step 4: 改 memory/store.go ListLogs**

签名改 `(ctx, appID, targetType, targetID, level, q string, limit int)`，过滤逻辑头部加：

```go
// targetType=dataservice 走 TargetType/TargetID 维度；否则走原 appID 维度（向后兼容）。
if targetType == observability.TargetDataservice {
	if targetID != "" && l.TargetID != targetID {
		continue
	}
	if l.TargetType != observability.TargetDataservice {
		continue
	}
} else {
	if appID != "" && l.AppID != appID {
		continue
	}
}
```
（替换原 `if appID != "" && l.AppID != appID { continue }` 那一行）

- [ ] **Step 5: 改 compose.go ListLogs 透传**

```go
func (r *Repo) ListLogs(ctx context.Context, appID, targetType, targetID, level, q string, limit int) ([]observability.LogEntry, error) {
	return r.logs.ListLogs(ctx, appID, targetType, targetID, level, q, limit)
}
```

- [ ] **Step 6: 改 handler.go serveLogs 读 query**

```go
logs, err := h.repo.ListLogs(r.Context(), q.Get("appId"), q.Get("targetType"), q.Get("targetId"), q.Get("level"), q.Get("q"), limit)
```

- [ ] **Step 7: 修所有调用点编译**

Run: `go build ./... 2>&1 | grep -i listlogs`
逐个修（real/logs.go ListLogs 签名在 Task 4 改，此处临时透传 targetType/targetID 占位）。real/logs.go 签名先改：

```go
func (s *LogsStore) ListLogs(ctx context.Context, appID, targetType, targetID, level, q string, limit int) ([]observability.LogEntry, error) {
	_ = targetType
	_ = targetID
	// ... 原 body 不变
```

- [ ] **Step 8: 运行测试通过**

Run: `go test ./internal/observability/... -run TestListLogs -v`
Expected: PASS（含新 TestListLogsDataserviceTarget + 既有 logs 测试）。

- [ ] **Step 9: Commit**

```bash
git add internal/observability/
git commit -m "feat(observability): logs 加 targetType/targetId 维度（支撑 dataservice 日志）"
```

---

## Task 4: real/logs.go 按 targetType 选 pod 正则

**Files:**
- Modify: `internal/observability/real/logs.go:48`（ListLogs 函数）

**Interfaces:**
- Consumes: Task 3 的 `targetType, targetID` 参数。
- Produces: dataservice 维度日志查询能力（Pod 名 `ds-<id>-0`）。

- [ ] **Step 1: 改 ListLogs pod 正则逻辑**

替换 ListLogs 开头 podRegex 选择逻辑（在 `podRegex := "wl-.*"` 那段附近），加 dataservice 分支：

```go
podRegex := "wl-.*"
if targetType == observability.TargetDataservice {
	if targetID == "" {
		return []observability.LogEntry{}, nil // dataservice 需指定 targetID
	}
	// 数据服务 STS Pod 名 = <ds-id>-0
	podRegex = regexp.QuoteMeta(targetID) + "-\\d+"
} else if appID != "" {
	if s.lister == nil {
		return []observability.LogEntry{}, nil
	}
	ids, err := s.lister.AppWorkloadIDs(ctx, appID)
	if err != nil || len(ids) == 0 {
		return []observability.LogEntry{}, nil
	}
	podRegex = lokiPodSelector(ids)
}
```

并在组装 LogEntry 时，dataservice 维度填 TargetType/TargetID（app 维度保持 AppID）：

```go
le := observability.LogEntry{
	ID:        fmt.Sprintf("%s/%s", pod, val[0]),
	Level:     inferLevel(msg),
	Message:   msg,
	TraceID:   r.Stream["trace_id"],
	Timestamp: time.Unix(0, tsNs),
}
if targetType == observability.TargetDataservice {
	le.TargetType = observability.TargetDataservice
	le.TargetID = targetID
} else {
	le.AppID = appID
}
out = append(out, le)
```

- [ ] **Step 2: 删除占位 `_ = targetType; _ = targetID`（Task 3 临时加的）**

- [ ] **Step 3: 编译 + 既有测试**

Run: `go build ./internal/observability/real/... && go test ./internal/observability/...`
Expected: 通过。

- [ ] **Step 4: Commit**

```bash
git add internal/observability/real/logs.go
git commit -m "feat(observability): real logs 按 targetType 选 pod 正则（dataservice 支持）"
```

---

## Task 5: alerts handler 加 targetType/targetId 过滤

**Files:**
- Modify: `internal/observability/handler.go:198`（serveAlerts）
- Modify: `internal/observability/compose/compose.go:59`（ListAlerts 加过滤参数）
- Modify: `internal/observability/readers.go`（Repository.ListAlerts 签名——若 Repository 接口声明了 ListAlerts）
- Test: `internal/observability/compose/compose_test.go`、`internal/observability/handler_test.go`

**Interfaces:**
- Produces: `ListAlerts(ctx, targetType, targetID string)` 支持按 targetType/targetId 过滤；handler 从 `?targetType=&targetId=` 读取。
- 注：compose.ListAlerts 当前签名 `(ctx)`，扩参数；memory.ListAlerts 同步。

- [ ] **Step 1: 写失败测试（compose 过滤）**

在 `internal/observability/compose/compose_test.go` 追加：

```go
func TestListAlertsFiltersByTargetType(t *testing.T) {
	// 用既有 fakeMetrics + 一个 rule，构造 dataservice series，验证 targetType 过滤
	// 参照既有 TestListAlertsEvaluatesAgainstMetrics 的构造方式
}
```

（具体：参照文件顶部 `TestListAlertsEvaluatesAgainstMetrics` 的 fakeMetrics/rule 构造，灌一条 `TargetType:"dataservice"` series + 一条 `TargetType:"app"` series，验证 `ListAlerts(ctx, "dataservice", "")` 只返 dataservice 的 alert。）

- [ ] **Step 2: 改 compose.ListAlerts 签名 + 过滤**

```go
func (r *Repo) ListAlerts(ctx context.Context, targetType, targetID string) ([]observability.Alert, error) {
	// ... 取 rules + series（原逻辑）
	for _, rule := range rules {
		if !rule.Enabled { continue }
		for _, s := range series {
			if !rule.Matches(s) { continue }
			// targetType 过滤：非空时只返该类型
			if targetType != "" && s.TargetType != targetType { continue }
			if targetID != "" && s.TargetID != targetID { continue }
			if rule.Breached(s.Current) {
				alerts = append(alerts, /* ... 原构造 ... */)
			}
		}
	}
	// ... sort + return
}
```

- [ ] **Step 3: memory.ListAlerts 同步签名**

`func (s *Store) ListAlerts(ctx context.Context, targetType, targetID string) ([]observability.Alert, error)`，过滤逻辑同 compose（按 series 的 TargetType/TargetID）。memory 模式 series 永空故返空，但签名需对齐。

- [ ] **Step 4: handler serveAlerts 读 query**

```go
func (h *Handler) serveAlerts(w http.ResponseWriter, r *http.Request) {
	// ... method + allow
	q := r.URL.Query()
	alerts, err := h.repo.ListAlerts(r.Context(), q.Get("targetType"), q.Get("targetId"))
	// ...
}
```

- [ ] **Step 5: 修所有调用点（cmd/core / admin handler 若有调 ListAlerts）**

Run: `go build ./... 2>&1 | grep -i listalerts`
逐个透传 `""，""`（无过滤）。

- [ ] **Step 6: 运行测试通过**

Run: `go test ./internal/observability/... -run Alert -v`
Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add internal/observability/
git commit -m "feat(observability): alerts 加 targetType/targetId 过滤"
```

---

## Task 6: real/metrics.go dataservice 加 disk_usage + 引擎业务指标

**Files:**
- Modify: `internal/observability/real/metrics.go:166-243`（listDataserviceMetrics 的 `defs`）

**Interfaces:**
- Consumes: Task 1 的引擎业务指标常量 + `dataservice.Kind`/`Engine`（从数据服务 spec 解析——但 real/metrics 无 spec，需从 targetID 反查）。**关键问题**：real/metrics 只拿到 targetID（ds-id），不知道 kind/engine。**解法**：PromQL 不分 kind，全查所有引擎业务指标名（Prometheus 没有该 metric → 返空），由前端按 kind 选展示。即 `defs` 加所有业务指标 PromQL（不限 kind），查不到自然降级空。
- Produces: dataservice 真实业务指标 + PVC 用量。

- [ ] **Step 1: 改 listDataserviceMetrics defs，加 disk_usage + 业务指标**

`defs` 追加（pod=`<targetID>-0`，container="exporter" 为 sidecar，"main" 为引擎内置 metrics）：

```go
// PVC 磁盘使用率（kubelet volume metrics，无需 exporter；PVC 名 data-<ds-id>-0）
observability.MetricDiskUsage: {
	promQL: fmt.Sprintf(
		"kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q} / clamp_min(kubelet_volume_stats_capacity_bytes{namespace=%q,persistentvolumeclaim=%q}, 1) * 100",
		ns, "data-"+pod, ns, "data-"+pod,
	),
	unit: "%", scale: 1,
},
// 引擎业务指标：sidecar exporter（container=exporter）或引擎内置 /metrics（container=main）。
// 全查所有引擎指标名，Prometheus 无该 metric → 返空（诚实降级，前端按 kind 选展示）。
observability.MetricConnections: {fmt.Sprintf(`max(pg_stat_activity_count{namespace=%q,pod=%q}) or max(redis_connected_clients{namespace=%q,pod=%q}) or max(mysql_threads_connected{namespace=%q,pod=%q})`, ns, pod, ns, pod, ns, pod), "", 1},
observability.MetricQPS: {fmt.Sprintf(`sum(rate(pg_stat_database_xact_commit{namespace=%q,pod=%q}[5m])) or sum(rate(redis_commands_processed_total{namespace=%q,pod=%q}[5m])) or sum(rate(meili_search_total{namespace=%q,pod=%q}[5m]))`, ns, pod, ns, pod, ns, pod), "/s", 1},
observability.MetricHitRate: {fmt.Sprintf(`redis_keyspace_hit_rate{namespace=%q,pod=%q}`, ns, pod), "%", 1},
observability.MetricLag: {fmt.Sprintf(`nats_jetstream_pending{namespace=%q,pod=%q}`, ns, pod), "", 1},
observability.MetricVectors: {fmt.Sprintf(`qdrant_collection_vectors_count{namespace=%q,pod=%q}`, ns, pod), "", 1},
```

- [ ] **Step 2: 改 names 默认列表**

`names` 默认从 `[cpu, mem, disk_io, net_io]` 扩为含新指标：

```go
names = []string{
	observability.MetricCPU, observability.MetricMem, observability.MetricDiskIO, observability.MetricNetIO,
	observability.MetricDiskUsage,
	observability.MetricConnections, observability.MetricQPS, observability.MetricHitRate, observability.MetricLag, observability.MetricVectors,
}
```

- [ ] **Step 3: 编译**

Run: `go build ./internal/observability/real/...`
Expected: 通过。

- [ ] **Step 4: 单元测试（PromQL 构造）**

若 real/metrics 无 fake Prometheus 测试基础设施，则验证 PromQL 字符串构造正确（抽 `defs` 构造为可测函数 `dataserviceDefs(ns, pod)` 返 map，单测断言 key 存在 + 字符串含 `kubelet_volume_stats`）。

在 `internal/observability/real/metrics_test.go`（若不存在新建）加：

```go
func TestDataserviceDefsContainsDiskUsage(t *testing.T) {
	defs := dataserviceDefs("paas-t-acme", "ds-1-0")
	if _, ok := defs["disk_usage"]; !ok {
		t.Fatal("缺 disk_usage")
	}
	if !strings.Contains(defs["disk_usage"].promQL, "kubelet_volume_stats_used_bytes") {
		t.Fatalf("disk_usage PromQL 应查 kubelet_volume_stats：%s", defs["disk_usage"].promQL)
	}
	if _, ok := defs["connections"]; !ok {
		t.Fatal("缺 connections")
	}
}
```

（需把 listDataserviceMetrics 内的 `defs` 字面量抽成 `dataserviceDefs(ns, pod string) map[string]metricDef` 函数，供单测。）

- [ ] **Step 5: 运行测试**

Run: `go test ./internal/observability/real/ -run DataserviceDefs -v`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/observability/real/metrics.go internal/observability/real/metrics_test.go
git commit -m "feat(observability): dataservice 指标加 PVC 用量 + 引擎业务指标 PromQL"
```

---

## Task 7: dataservice handler 加 PodReader 接口 + pods 端点

**Files:**
- Modify: `internal/dataservice/handler.go`（Handler struct + WithPodReader + serveItem pods 分支 + servePods）
- Modify: `internal/dataservice/admin_handler.go`（`Instance`/`PodInfo` 类型——复用或新增）
- Test: `internal/dataservice/handler_test.go`

**Interfaces:**
- Produces: `dataservice.PodReader` 接口 + `dataservice.PodInfo` 类型 + `GET /api/dataservices/{id}/pods` 端点；`WithPodReader(r PodReader) HandlerOpt`。
- Consumes: `workload.Instance` 风格（不直接 import workload，自定义 PodInfo）。

- [ ] **Step 1: 定义 PodInfo + PodReader 接口**

在 `internal/dataservice/admin_handler.go`（InstanceInfo 旁）加：

```go
// PodInfo 是数据服务的一个运行实例（Pod 级），用于依赖资源排障。对齐 workload.Instance 字段语义。
type PodInfo struct {
	Name      string `json:"name"`               // Pod 名
	Status    string `json:"status"`             // Pending/Running/Failed/Unknown
	Ready     string `json:"ready,omitempty"`    // "1/1"
	Restarts  int    `json:"restarts"`           // 重启次数
	Node      string `json:"node,omitempty"`     // 节点
	IP        string `json:"ip,omitempty"`       // Pod IP
	Age       string `json:"age,omitempty"`      // 启动至今时长（人类可读）
	Message   string `json:"message,omitempty"`  // 状态原因
}

// PodReader 读数据服务运行 Pod（排障用）。cmd/core 桥接 K8s clientset。
// nil 或读不到时返空（集群外降级），不报错。
type PodReader interface {
	Pods(ctx context.Context, namespace, dataserviceID string) ([]PodInfo, error)
}
```

- [ ] **Step 2: Handler struct 加 podReader 字段 + WithPodReader**

`internal/dataservice/handler.go` Handler struct 加 `podReader PodReader`，opts 区加：

```go
// WithPodReader 注入 Pod 读取器（K8s 模式查 STS Pod）；nil 时 pods 端点返空。
func WithPodReader(r PodReader) HandlerOpt {
	return func(h *Handler) { h.podReader = r }
}
```

- [ ] **Step 3: serveItem 加 pods 分支**

在 serveItem 的 switch action 内（`case "upgrade":` 后）加：

```go
case "pods":
	h.servePods(w, r, id)
```

- [ ] **Step 4: 写 servePods**

```go
// servePods 处理 GET /api/dataservices/{id}/pods：返该数据服务的运行 Pod（排障用）。
// 越权校验：先 Get 数据服务确认租户归属（跨租户统一 not found 不泄漏）。
// reader nil（集群外）返空切片 200（降级，与 restart 同款）。
func (h *Handler) servePods(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermDataServiceRead) {
		return
	}
	d, err := h.repo.Get(r.Context(), id) // Get 自带 ctx tenant 过滤，跨租户 NotFound
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if h.podReader == nil {
		httputil.WriteData(w, []PodInfo{}) // 集群外降级返空
		return
	}
	// namespace 由 reader 从 ctx tenant 解析（cmd/core 桥接内 tenant.Namespace(tid)）
	pods, err := h.podReader.Pods(r.Context(), "", d.ID) // namespace 传空，reader 内部用 ctx
	if err != nil {
		httputil.WriteData(w, []PodInfo{}) // best-effort 降级
		return
	}
	httputil.WriteData(w, pods)
}
```

- [ ] **Step 5: 写失败测试**

`internal/dataservice/handler_test.go` 加：

```go
type fakePodReader struct{ pods []PodInfo }
func (f fakePodReader) Pods(ctx context.Context, ns, id string) ([]PodInfo, error) { return f.pods, nil }

func TestServePodsReturnsInstances(t *testing.T) {
	repo := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	d, _ := repo.Create(ctx, DataService{Kind: "db", Name: "pg1", Spec: map[string]string{"engine": "postgres"}, EnvID: "env1"})
	h := NewHandler(repo, WithPodReader(fakePodReader{pods: []PodInfo{{Name: "pg1-0", Status: "Running", Ready: "1/1"}}}))
	req := httptest.NewRequest("GET", "/api/dataservices/"+d.ID+"/pods", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 { t.Fatalf("code=%d", w.Code) }
	var resp struct{ Data []PodInfo `json:"data"` }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "pg1-0" {
		t.Fatalf("期望 pg1-0，实际 %+v", resp.Data)
	}
}

func TestServePodsNilReaderReturnsEmpty(t *testing.T) {
	repo := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	d, _ := repo.Create(ctx, DataService{Kind: "db", Name: "pg1", Spec: map[string]string{"engine": "postgres"}, EnvID: "env1"})
	h := NewHandler(repo) // 无 PodReader
	req := httptest.NewRequest("GET", "/api/dataservices/"+d.ID+"/pods", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 { t.Fatalf("nil reader 应降级 200，code=%d", w.Code) }
}
```

- [ ] **Step 6: 运行测试**

Run: `go test ./internal/dataservice/ -run ServePods -v`
Expected: PASS。

- [ ] **Step 7: 全包测试 + 编译**

Run: `go test ./internal/dataservice/... && go build ./...`
Expected: 通过。

- [ ] **Step 8: Commit**

```bash
git add internal/dataservice/
git commit -m "feat(dataservice): 新增 GET /{id}/pods 端点 + PodReader 接口"
```

---

## Task 8: controller K8sPodReader 实现 + cmd/core 桥接注入

**Files:**
- Create: `internal/controller/dataservice_status.go`
- Modify: `cmd/core/main.go`（main.go:591 附近 dsOpts 加 WithPodReader；admin dsOpts 同）

**Interfaces:**
- Consumes: Task 7 的 `dataservice.PodReader` + `PodInfo`。
- Produces: `controller.NewK8sPodReader(cl client.Client) *K8sPodReader`，实现 `Pods(ctx, ns, dsID)`。

- [ ] **Step 1: 写 K8sPodReader（参照 workload K8sStatusReader）**

`internal/controller/dataservice_status.go`（新建）：

```go
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// K8sPodReader 查数据服务的运行 Pod（排障用），实现 dataservice.PodReader。
// 按 label paas.aitoys/dataservice=<dsID> 查 Pod（reconciler dataServiceLabels 设此 label）。
type K8sPodReader struct {
	client client.Client
}

func NewK8sPodReader(cl client.Client) *K8sPodReader {
	return &K8sPodReader{client: cl}
}

// namespace 传空时从 ctx tenant 解析（paas-<tenant>）。
func (r *K8sPodReader) Pods(ctx context.Context, namespace, dsID string) ([]dataservice.PodInfo, error) {
	out := []dataservice.PodInfo{}
	if r == nil || r.client == nil || dsID == "" {
		return out, nil // 集群外降级
	}
	if namespace == "" {
		tid, _ := tenant.TenantFrom(ctx)
		namespace = tenant.Namespace(tid)
	}
	pods := &corev1.PodList{}
	labelSel := client.MatchingLabels{labels.KeyDataservice: dsID}
	if err := r.client.List(ctx, pods, client.InNamespace(namespace), labelSel); err != nil {
		return out, nil // best-effort
	}
	for _, p := range pods.Items {
		out = append(out, dataservice.PodInfo{
			Name:     p.Name,
			Status:   string(p.Status.Phase),
			Ready:    readyString(p.Status.ContainerStatuses),
			Restarts: restartSum(p.Status.ContainerStatuses),
			Node:     p.Spec.NodeName,
			IP:       p.Status.PodIP,
			Age:      ageHuman(p.CreationTimestamp),
			Message:  podMessage(p.Status),
		})
	}
	return out, nil
}

func readyString(cs []corev1.ContainerStatus) string {
	if len(cs) == 0 {
		return ""
	}
	ready := 0
	for _, c := range cs {
		if c.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(cs))
}

func restartSum(cs []corev1.ContainerStatus) int {
	n := 0
	for _, c := range cs {
		n += int(c.RestartCount)
	}
	return n
}

func ageHuman(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t.Time).Round(time.Second)
	return d.String()
}

func podMessage(s corev1.PodStatus) string {
	if len(s.ContainerStatuses) > 0 {
		cs := s.ContainerStatuses[0]
		if cs.State.Waiting != nil && cs.State.Waiting.Message != "" {
			return cs.State.Waiting.Message
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
			return cs.State.Terminated.Message
		}
	}
	return ""
}
```

- [ ] **Step 2: cmd/core 桥接注入**

`cmd/core/main.go`，在两处 dsOpts 构造（约 L491 租户端、L599 admin 端）加：

```go
dataservice.WithPodReader(controller.NewK8sPodReader(appliers.client)),
```

（参照 `dataservice.WithRestarter(appliers.dsRestarter)` 的 typed-nil 防御模式——若 `appliers.client` 可能为 nil，`NewK8sPodReader(nil)` 返的实例 Pods 内部 `r.client == nil` 判空降级，安全。）

- [ ] **Step 3: 确认 appliers.client 字段存在**

Run: `grep -n "client " cmd/core/manager.go cmd/core/persistence.go | head`
确认 `appliers.client client.Client` 字段（governance RouteApplier 已加过，应已存在）。

- [ ] **Step 4: 编译**

Run: `go build ./...`
Expected: 通过。

- [ ] **Step 5: Commit**

```bash
git add internal/controller/dataservice_status.go cmd/core/main.go
git commit -m "feat(controller): K8sPodReader 实现 + cmd/core 注入 dataservice pods 端点"
```

---

## Task 9: dataservice STS exporter sidecar 注入

**Files:**
- Modify: `internal/controller/dataservice_controller.go`（加 `exporterSidecar(d)` + `exporterImage(kind,engine,registry)` + STS template 注入）
- Test: `internal/controller/dataservice_controller_test.go`

**Interfaces:**
- Produces: STS 含 `main` + 可选 `exporter` sidecar；sidecar 端口名 `exporter` 端口 9100。

- [ ] **Step 1: 加 exporterImage + exporterSidecar 函数**

在 `dataservice_controller.go`（`engineImage` 旁）加：

```go
// exporterImage 按 Kind+Engine 选 exporter 镜像（sidecar 注入用）。
// minio/qdrant/meilisearch 引擎内置 /metrics（无需 sidecar）返空。
// registry 非空时内网化（与 engineImage 同款 library/<name>:<tag>）。
func exporterImage(kind, engine, registry string) string {
	var img string
	switch kind {
	case "db":
		if engine == "postgres" {
			img = "prometheuscommunity/postgres-exporter:v0.15.0"
		} else if engine == "mysql" {
			img = "prom/mysqld-exporter:v0.15.1"
		}
	case "cache":
		img = "oliver006/redis_exporter:v1.62.0" // redis/valkey 共用
	case "mq":
		if engine == "nats" {
			img = "natsio/prometheus-nats-exporter:0.16.0"
		}
	}
	if img == "" {
		return "" // storage/vector/search 引擎内置 metrics，无 sidecar
	}
	if registry == "" {
		return img
	}
	name := img
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return registry + "/library/" + name
}

// exporterSidecar 按 Kind+Engine 构造 exporter sidecar 容器（无则返 nil）。
// 凭证复用主容器 Secret（secretKeyRef，不重新生成）。sidecar 名固定 exporter，端口 9100。
func exporterSidecar(d *v1alpha1.DataService, registry string) *corev1.Container {
	img := exporterImage(d.Spec.Kind, d.Spec.Engine, registry)
	if img == "" {
		return nil // 引擎内置 metrics，无 sidecar
	}
	secretName := d.Name + "-secret"
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key}
	}
	envFrom := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref(key)}}
	}
	port := dataservice.EnginePort(d.Spec.Kind, d.Spec.Engine)
	c := &corev1.Container{
		Name:            "exporter",
		Image:           img,
		ImagePullPolicy: corev1.PullAlways,
		Ports:           []corev1.ContainerPort{{Name: "exporter", ContainerPort: 9100}},
		Resources:       defaultResources(),
	}
	switch d.Spec.Kind {
	case "db":
		if d.Spec.Engine == "postgres" {
			// pg exporter: DATA_SOURCE_NAME=postgresql://user:pwd@localhost:5432/db?sslmode=disable
			c.Env = []corev1.EnvVar{
				{Name: "DATA_SOURCE_NAME", Value: fmt.Sprintf("postgresql://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:%d/$(POSTGRES_DB)?sslmode=disable", port)},
				envFrom("POSTGRES_USER", "user"),
				envFrom("POSTGRES_PASSWORD", "password"),
				envFrom("POSTGRES_DB", "database"),
			}
		} else { // mysql
			c.Env = []corev1.EnvVar{
				{Name: "DATA_SOURCE_NAME", Value: fmt.Sprintf("root:$(MYSQL_ROOT_PASSWORD)@(localhost:%d)/", port)},
				envFrom("MYSQL_ROOT_PASSWORD", "password"),
			}
		}
	case "cache": // redis/valkey
		c.Env = []corev1.EnvVar{
			{Name: "REDIS_ADDR", Value: fmt.Sprintf("redis://localhost:%d", port)},
			envFrom("REDIS_PASSWORD", "password"),
		}
	case "mq": // nats
		c.Args = []string{"-connz", "-jsz", "-serverz", fmt.Sprintf("-server=nats://localhost:%d", port)}
	}
	return c
}
```

- [ ] **Step 2: STS template 注入 sidecar**

在 `applyStatefulSet` 的 mutate 回调内，改 PodTemplateSpec 构造（原 `Containers: []corev1.Container{containerFor(d, image)}`）：

```go
containers := []corev1.Container{containerFor(d, image)}
if sc := exporterSidecar(d, os.Getenv("PAAS_IMAGE_REGISTRY")); sc != nil {
	containers = append(containers, *sc)
}
tmpl := corev1.PodTemplateSpec{
	ObjectMeta: metav1.ObjectMeta{Labels: labels},
	Spec:       corev1.PodSpec{Containers: containers},
}
```

- [ ] **Step 3: 写测试（fake client，断言 sidecar 注入）**

`internal/controller/dataservice_controller_test.go` 加：

```go
func TestExporterSidecarInjectedForPostgres(t *testing.T) {
	cl, _ := newFakeClient(t) // 参照既有 dataservice_controller_test 的 fake client 构造
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-pg", Namespace: "paas-t-acme"},
		Spec: v1alpha1.DataServiceSpec{Kind: "db", Engine: "postgres", TenantID: "t-acme",
			Connection: map[string]string{"user": "u", "password": "p", "database": "db"}},
	}
	cl.Create(context.Background(), d)
	r := &DataServiceReconciler{Client: cl, Scheme: cl.Scheme()}
	r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})

	sts := &appsv1.StatefulSet{}
	cl.Get(context.Background(), client.ObjectKey{Name: "ds-pg", Namespace: "paas-t-acme"}, sts)
	names := []string{}
	for _, c := range sts.Spec.Template.Spec.Containers {
		names = append(names, c.Name)
	}
	if !contains(names, "exporter") {
		t.Fatalf("postgres 应注入 exporter sidecar，实际容器：%v", names)
	}
}

func TestNoSidecarForMinio(t *testing.T) {
	// storage=minio 内置 metrics，断言无 exporter 容器
	// 同上构造 kind=storage engine=minio，断言 names 不含 exporter
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/controller/ -run ExporterSidecar -v && go test ./internal/controller/ -run NoSidecar -v`
Expected: PASS。

- [ ] **Step 5: 全 controller 测试回归**

Run: `go test ./internal/controller/...`
Expected: PASS（既有 reconciler 测试不破——sidecar 注入对它们透明，除非有断言容器数的，需更新）。

- [ ] **Step 6: Commit**

```bash
git add internal/controller/dataservice_controller.go internal/controller/dataservice_controller_test.go
git commit -m "feat(controller): dataservice STS 注入引擎 exporter sidecar"
```

---

## Task 10: Prometheus scrape 配置加 sidecar exporter 发现

**Files:**
- Modify: `deploy/observability/prometheus-values.yaml`（extraScrapeConfigs 或 relabel）

**Interfaces:**
- Produces: Prometheus 自动 scrape `paas.aitoys/managed-by=paas` Pod 的 exporter 端口。

- [ ] **Step 1: 查现有 scrape 配置**

Run: `cat deploy/observability/prometheus-values.yaml | grep -A20 extraScrape`

- [ ] **Step 2: 加 kubernetes-pods-exporter job（若不存在）**

在 `extraScrapeConfigs` 加（按端口名 exporter 发现）：

```yaml
- job_name: 'kubernetes-pods-exporter'
  kubernetes_sd_configs:
    - role: pod
  relabel_configs:
    - action: keep
      source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_managed_by]
      regex: paas
    - action: keep
      source_labels: [__meta_kubernetes_pod_container_port_name]
      regex: exporter
    - source_labels: [__meta_kubernetes_namespace]
      target_label: namespace
    - source_labels: [__meta_kubernetes_pod_label_paas_aitoys_tenant]
      target_label: paas_aitoys_tenant
    - source_labels: [__meta_kubernetes_pod_label_paas_aitoys_dataservice]
      target_label: paas_aitoys_dataservice
```

- [ ] **Step 3: 部署后验证（留 Task 14 集成验证执行）**

本任务仅改配置文件，验证合并到 Task 14。

- [ ] **Step 4: Commit**

```bash
git add deploy/observability/prometheus-values.yaml
git commit -m "feat(observability): prometheus scrape 加 dataservice exporter sidecar 发现"
```

---

## Task 11: 前端 useUrlState composable

**Files:**
- Create: `frontend/console-user/src/composables/useUrlState.ts`

**Interfaces:**
- Produces: `useUrlState<T>(key, defaultValue)` 返 `{ value: Ref<T> }`，双向同步 router.query[key]。

- [ ] **Step 1: 写 composable**

```ts
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

// useUrlState 双向同步某状态项 ↔ URL query key。
// 读：route.query[key] ?? defaultValue；写：watch value → router.replace（避免历史栈膨胀）；
// 回：watch route.query[key] → value（前进/后退同步）。
// 空字符串值写 URL 时省略（保持 URL 干净）。
export function useUrlState<T extends string>(key: string, defaultValue: T) {
  const route = useRoute()
  const router = useRouter()

  const read = (): T => {
    const v = route.query[key]
    return (typeof v === 'string' ? (v as T) : defaultValue) ?? defaultValue
  }

  const value = ref<T>(read()) as { value: T }

  // value → URL
  watch(
    value,
    (v) => {
      const cur = route.query[key]
      const next = v === defaultValue ? undefined : v
      if ((cur ?? '') !== (next ?? '')) {
        router.replace({ query: { ...route.query, [key]: next } })
      }
    },
    { flush: 'post' },
  )

  // URL → value（前进/后退）
  watch(
    () => route.query[key],
    (q) => {
      const v = (typeof q === 'string' ? q : defaultValue) ?? defaultValue
      if (v !== value.value) value.value = v
    },
  )

  return { value }
}
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend/console-user && pnpm vue-tsc --noEmit 2>&1 | grep useUrlState`
Expected: 无错误（或文件不在 build 入口则单独 `tsc --noEmit` 该文件）。

- [ ] **Step 3: Commit**

```bash
git add frontend/console-user/src/composables/useUrlState.ts
git commit -m "feat(console-user): useUrlState composable 双向同步状态↔URL query"
```

---

## Task 12: envStore env 进 URL + App.vue 恢复

**Files:**
- Modify: `frontend/console-user/src/stores/env.ts`
- Modify: `frontend/console-user/src/views/App.vue`（或路由守卫）

**Interfaces:**
- Produces: switchEnv 写 `env=<id>` 到 URL；启动/路由变化从 `route.query.env` 恢复 envStore。

- [ ] **Step 1: envStore 加 URL 同步**

env.ts 顶部 import `{ useRoute, useRouter }`。switchEnv 成功后写 URL（需在 store 外做，因 store 不持有 route——改在调用方或 App.vue watch envStore.currentEnvId 写 URL）。

**决策**：不在 store 内直接用 route（store 不应依赖 router）。改在 `App.vue` watch envStore.currentEnvId → 写 URL；App.vue 初始化从 query 恢复。

- [ ] **Step 2: App.vue 加双向同步**

`App.vue` setup 内：

```ts
import { useEnvStore } from '@/stores/env'
import { useRoute, useRouter } from 'vue-router'
const envStore = useEnvStore()
const route = useRoute()
const router = useRouter()

// envStore.currentEnvId → URL
watch(() => envStore.currentEnvId, (id) => {
  const cur = route.query.env ?? ''
  if ((id || '') !== cur) {
    router.replace({ query: { ...route.query, env: id || undefined } })
  }
})

// envStore.loadEnvs 完成后从 URL 恢复（一次）
onMounted(async () => {
  if (!envStore.envs.length) await envStore.loadEnvs()
  const q = route.query.env as string
  if (q) {
    const found = envStore.envs.find((e) => e.id === q)
    if (found) await envStore.switchEnv(found)
  }
})
```

- [ ] **Step 3: 移除 Workloads.vue 现有的 `route.query.env` 读取（去重，统一走 envStore）**

`Workloads.vue` onMounted 内的 `const q = route.query.env as string; if (q) {...switchEnv...}` 段删除（App.vue 统一处理，避免重复 switchEnv）。

- [ ] **Step 4: 构建验证**

Run: `cd frontend/console-user && pnpm build 2>&1 | tail -5`
Expected: 通过。

- [ ] **Step 5: Commit**

```bash
git add frontend/console-user/src/stores/env.ts frontend/console-user/src/views/App.vue frontend/console-user/src/views/Workloads.vue
git commit -m "feat(console-user): envStore 环境上下文进 URL（分享链接保留环境）"
```

---

## Task 13: Applications/Workloads/数据服务列表 筛选进 URL

**Files:**
- Modify: `frontend/console-user/src/views/Applications.vue`（搜索词 q）
- Modify: `frontend/console-user/src/views/Workloads.vue`（type 筛选）
- Modify: `frontend/console-user/src/views/DataServices.vue`（搜索词 q，若有）

**Interfaces:**
- Consumes: Task 11 的 `useUrlState`。

- [ ] **Step 1: Applications.vue 搜索词接 useUrlState**

定位 `const q = (route.query.q ?? '').toString()`（约 L85）的搜索逻辑，改：

```ts
import { useUrlState } from '@/composables/useUrlState'
const { value: searchQ } = useUrlState('q', '')
const filteredApps = computed(() => {
  const kw = searchQ.value.toLowerCase().trim()
  return apps.value.filter((a) => a.name.toLowerCase().includes(kw) || a.id.toLowerCase().includes(kw))
})
```

搜索框 `<el-input v-model="searchQ">`。

- [ ] **Step 2: Workloads.vue type 筛选接 useUrlState**

```ts
const { value: activeType } = useUrlState<'service' | 'job' | 'cronjob'>('type', 'service')
```
（替换原 activeType ref；环境走 envStore，app 走 ?app 已有保留。）

- [ ] **Step 3: DataServices.vue 若有搜索词同样接入**

Run: `grep -n "keyword\|search\|el-input" frontend/console-user/src/views/DataServices.vue`，若有则接 useUrlState('q','')。

- [ ] **Step 4: 构建验证**

Run: `cd frontend/console-user && pnpm build`
Expected: 通过。

- [ ] **Step 5: Commit**

```bash
git add frontend/console-user/src/views/
git commit -m "feat(console-user): 列表筛选（搜索词/type）进 URL（分享定位）"
```

---

## Task 14: AppObservability 依赖资源 tab 重构

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/AppObservability.vue`（依赖资源 tab 段，约 L295-340 + loadDeps）

**Interfaces:**
- Consumes: Task 1 引擎指标名（前端硬编码同名字符串）+ Task 7 pods 端点 + Task 3/4/5 logs/alerts targetType。

- [ ] **Step 1: 加按 kind 动态指标配置**

AppObservability.vue script 顶部加：

```ts
interface MetricDef { name: string; label: string }
const DEP_METRICS: Record<string, MetricDef[]> = {
  db:      [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'connections',label:'连接数'},{name:'qps',label:'QPS'},{name:'disk_io',label:'磁盘IO'},{name:'net_io',label:'网络IO'}],
  cache:   [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'hit_rate',label:'命中率'},{name:'qps',label:'QPS'},{name:'connections',label:'连接数'}],
  mq:      [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'connections',label:'连接数'},{name:'lag',label:'堆积'},{name:'qps',label:'QPS'}],
  storage: [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'disk_io',label:'磁盘IO'},{name:'net_io',label:'网络IO'}],
  vector:  [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'qps',label:'检索QPS'},{name:'vectors',label:'向量数'},{name:'disk_io',label:'磁盘IO'}],
  search:  [{name:'cpu',label:'CPU'},{name:'mem',label:'内存'},{name:'qps',label:'QPS'},{name:'disk_io',label:'磁盘IO'}],
}
function depMetricDefs(kind: string): MetricDef[] { return DEP_METRICS[kind] ?? DEP_METRICS.storage }
```

扩 DepMetric 接口加 connections/qps/hit_rate/lag/vectors/disk_usage/pods/alerts/log：

```ts
interface DepMetric {
  id: string; kind: string; name: string; status: string
  cpu?: MetricSeries; mem?: MetricSeries; disk_io?: MetricSeries; net_io?: MetricSeries
  connections?: MetricSeries; qps?: MetricSeries; hit_rate?: MetricSeries; lag?: MetricSeries; vectors?: MetricSeries
  diskUsage?: number  // PVC 用量 %
  pods: PodInfo[]; alerts: AlertItem[]; logs: LogEntry[]
}
interface PodInfo { name: string; status: string; ready?: string; restarts: number; node?: string; age?: string }
interface AlertItem { ruleName: string; severity: string; metricName: string; value: number }
```

- [ ] **Step 2: loadDeps 扩展加载 pods/alerts/logs + disk_usage**

loadDeps 内每资源并行加：

```ts
const [podsR, alertsR, logsR] = await Promise.all([
  fetchAuth(`/api/dataservices/${ds.id}/pods`),
  fetchAuth(`/api/observability/alerts?targetType=dataservice&targetId=${ds.id}`),
  fetchAuth(`/api/observability/logs?targetType=dataservice&targetId=${ds.id}&limit=3`),
])
const pods = podsR.ok ? (await podsR.json()).data ?? [] : []
const alerts = alertsR.ok ? (await alertsR.json()).data ?? [] : []
const logs = logsR.ok ? (await logsR.json()).data ?? [] : []
const diskUsage = series.find((m) => m.name === 'disk_usage')?.current
out.push({ /* ...原字段... */ pods, alerts, logs, diskUsage, connections: series.find(m=>m.name==='connections'), /* ...其余业务指标... */ })
```

- [ ] **Step 3: 模板重构 dep-card**

替换 dep-card 内 `.dep-metrics`（原写死 4 项）为按 kind 动态 + 加 Pod/告警/日志/下钻：

```vue
<div class="dep-head">
  <span class="dep-kind">{{ kindLabel[d.kind] || d.kind }}</span>
  <span class="dep-name mono">{{ d.name }}</span>
  <el-tag :type="d.status === 'running' ? 'success' : 'info'" size="small">{{ d.status }}</el-tag>
  <span v-if="d.diskUsage != null" class="dep-pvc">磁盘 {{ Math.round(d.diskUsage) }}%</span>
  <el-button text type="primary" size="small" style="margin-left:auto" @click="goDep(d)">详情 →</el-button>
</div>
<div class="dep-metrics">
  <div v-for="m in depMetricDefs(d.kind)" :key="m.name" class="dep-metric">
    <span class="dm-label">{{ m.label }}</span>
    <span class="mono dm-value">{{ depMetricVal(d, m.name) }}<span class="dm-unit">{{ depMetricUnit(d, m.name) }}</span></span>
    <div v-if="depMetricSeries(d, m.name)" class="spark">
      <span v-for="(h, idx) in sparkHeights(depMetricSeries(d, m.name)!.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
    </div>
  </div>
</div>
<div v-if="d.pods.length" class="dep-pods">
  <span v-for="p in d.pods" :key="p.name" class="pod-chip" :class="podClass(p)">
    ●{{ p.status }}<span v-if="p.restarts"> · {{p.restarts}}重启</span>
  </span>
</div>
<div v-if="d.alerts.length" class="dep-alerts">
  <span v-for="a in d.alerts" :key="a.ruleName" class="alert-chip" :class="a.severity">⚠ {{ a.ruleName }} ({{ a.metricName }}={{ Math.round(a.value) }})</span>
</div>
<div v-if="d.logs.length" class="dep-logs">
  <div v-for="l in d.logs.slice(0,3)" :key="l.id" class="log-line" :class="l.level">
    <span class="log-lvl">{{ l.level }}</span> <span class="mono">{{ l.message.slice(0,120) }}</span>
  </div>
</div>
<el-button text type="primary" size="small" @click="goDep(d)">查看完整监控/日志/告警 →</el-button>
```

加 helper：

```ts
function depMetricSeries(d: DepMetric, name: string): MetricSeries | undefined {
  return (d as any)[metricKey(name)] as MetricSeries | undefined
}
function metricKey(name: string): string {
  return { disk_io:'disk_io', net_io:'net_io', hit_rate:'hit_rate' }[name] ?? name
}
function depMetricVal(d: DepMetric, name: string): string {
  const s = depMetricSeries(d, name); return s ? fmtVal(s.current) : '—'
}
function depMetricUnit(d: DepMetric, name: string): string {
  return depMetricSeries(d, name)?.unit ?? ''
}
function goDep(d: DepMetric) {
  router.push(`/resources/${d.kind}/${d.id}?app=${props.appId}`)
}
function podClass(p: PodInfo): string {
  return p.status === 'Running' ? 'ok' : (p.status === 'Failed' ? 'err' : 'warn')
}
```

- [ ] **Step 4: 加 CSS（dep-pvc/dep-pods/pod-chip/dep-alerts/alert-chip/dep-logs/log-line）**

style 段追加（复用既有色调变量）：

```css
.dep-pvc { font-size: 11px; color: var(--text-dim); margin-left: 6px; }
.dep-pods { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.pod-chip { font-size: 11px; padding: 1px 6px; border-radius: 3px; background: var(--surface-2, var(--surface)); }
.pod-chip.ok { color: var(--success); } .pod-chip.err { color: var(--danger); } .pod-chip.warn { color: var(--warning); }
.dep-alerts { margin-top: 6px; }
.alert-chip { font-size: 11px; margin-right: 6px; }
.alert-chip.critical { color: var(--danger); } .alert-chip.warning { color: var(--warning); }
.dep-logs { margin-top: 6px; max-height: 60px; overflow: auto; }
.log-line { font-size: 11px; color: var(--text-dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.log-line.error { color: var(--danger); }
.log-lvl { font-weight: 600; margin-right: 4px; }
```

- [ ] **Step 5: 构建验证**

Run: `cd frontend/console-user && pnpm build`
Expected: 通过（vue-tsc 类型断言 helper 用 any 兜底）。

- [ ] **Step 6: Commit**

```bash
git add frontend/console-user/src/views/app-tabs/AppObservability.vue
git commit -m "feat(console-user): 依赖资源 tab 重构（动态指标+Pod+告警+日志+下钻）"
```

---

## Task 15: DataServiceDetail 卡片错配修复

**Files:**
- Modify: `frontend/console-user/src/views/DataServiceDetail.vue:137`（metricOrder）

**Interfaces:**
- 无新接口，修既有错配。

- [ ] **Step 1: metricOrder 按 kind 动态选**

DataServiceDetail.vue 把 `const metricOrder = ['cpu','mem','rps','latency']` 改为按 props.kind 选（复用 AppObservability 同款 DEP_METRICS，或就地定义）：

```ts
const METRIC_ORDER: Record<string, string[]> = {
  db: ['cpu','mem','connections','qps','disk_io','net_io'],
  cache: ['cpu','mem','hit_rate','qps','connections'],
  mq: ['cpu','mem','connections','lag','qps'],
  storage: ['cpu','mem','disk_io','net_io'],
  vector: ['cpu','mem','qps','vectors','disk_io'],
  search: ['cpu','mem','qps','disk_io'],
}
const metricOrder = METRIC_ORDER[props.kind] ?? ['cpu','mem','disk_io','net_io']
```

metricLabel map 补全 connections/qps/hit_rate/lag/vectors/disk_io 中文标签。

- [ ] **Step 2: 构建验证**

Run: `cd frontend/console-user && pnpm build`
Expected: 通过。

- [ ] **Step 3: Commit**

```bash
git add frontend/console-user/src/views/DataServiceDetail.vue
git commit -m "fix(console-user): DataServiceDetail 指标卡按 kind 动态选（移除无意义 rps/latency）"
```

---

## Task 16: OpenAPI 登记 + 全量构建 + k8s 部署验证

**Files:**
- Modify: 后端 handler 路由登记（dataservice pods 已在 serveItem 内，无需单独 Operation；observability logs/alerts 新 query 参数 OpenAPI 描述可选）
- 无新文件。

- [ ] **Step 1: 全量后端测试**

Run: `make test`
Expected: 全绿（含新测试 + 既有回归，sidecar 注入对既有 controller 测试透明）。

- [ ] **Step 2: 三套前端构建**

Run: `cd frontend && pnpm build`
Expected: console-user/console-admin/landing 全通过。

- [ ] **Step 3: 部署 k8s**

Run: `./scripts/deploy-k8s.sh`
（k8s-always-latest 常设授权）

- [ ] **Step 4: e2e 验证 - sidecar exporter**

```bash
# 建一个 postgres 数据服务（或用现有）
kubectl get pods -n paas-t-acme -l paas.aitoys/dataservice=<ds-id> -o jsonpath='{.items[0].spec.containers[*].name}'
# 期望含 main + exporter（postgres）
# Prometheus targets 应含 kubernetes-pods-exporter job
curl -s http://prom.k8s.dd/api/v1/targets | grep exporter
```

- [ ] **Step 5: e2e 验证 - 依赖资源 tab**

浏览器 `/console/applications/<app>?tab=observability` → 依赖资源 tab → postgres 卡片应显示连接数/QPS（若 exporter 起）+ Pod + 日志；点「详情 →」跳 DataServiceDetail 带 `?app=`。

- [ ] **Step 6: e2e 验证 - 深链接**

复制 `/console/applications/<app>?tab=observability&env=<envId>` 到新窗口 → 直达可观测 tab + 该环境。分享 `/applications?q=shop` → 直达筛选 shop 的应用列表。

- [ ] **Step 7: e2e 验证 - pods/logs/alerts 端点**

```bash
curl -H "Authorization: Bearer sk-acme-admin" "http://paas.k8s.dd/api/dataservices/<ds-id>/pods"
curl -H "Authorization: Bearer sk-acme-admin" "http://paas.k8s.dd/api/observability/logs?targetType=dataservice&targetId=<ds-id>&limit=3"
curl -H "Authorization: Bearer sk-acme-admin" "http://paas.k8s.dd/api/observability/alerts?targetType=dataservice"
```
均 200。

- [ ] **Step 8: 无需 commit（部署验证步骤）**

---

## Self-Review 结果

**1. Spec coverage：**
- 依赖资源增强（指标/Pod/日志/告警/下钻）→ Task 1,6,7,8,14 ✓
- 引擎 exporter sidecar → Task 9,10 ✓
- PVC 用量 → Task 6 ✓
- pods 端点 → Task 7,8 ✓
- logs/alerts targetType 扩展 → Task 2,3,4,5 ✓
- 卡片错配修复 → Task 15 ✓
- 深链接（useUrlState + env + 列表筛选 + tab）→ Task 11,12,13 ✓（ApplicationDetail tab 已有，无需新任务）
- 横切约束（诚实降级/多租户/镜像内网化/best-effort）→ 各任务步骤内体现 ✓

**2. Placeholder scan：** Task 5 Step 1 的测试体标了「参照既有 TestListAlertsEvaluatesAgainstMetrics」——这是合理的（既有代码参照，非 placeholder），但执行时需 implementer 读既有测试。可接受。Task 8 Step 3 的 `appliers.client` 字段确认存在（governance RouteApplier 已加）。无 TBD/TODO。

**3. Type consistency：** `PodInfo`（Task 7 定义）↔ `K8sPodReader.Pods` 返 `[]dataservice.PodInfo`（Task 8）↔ 前端 `PodInfo`（Task 14）字段名一致（name/status/ready/restarts/node/age）。`useUrlState<T>`（Task 11）↔ 调用（Task 13）签名一致。`LogsReader.ListLogs` 新签名（Task 3）贯穿 compose/memory/real/handler（Task 3,4）+ cmd/core 调用点。`MetricConnections` 等常量（Task 1）↔ real/metrics defs（Task 6）↔ 前端 DEP_METRICS name（Task 14）字符串一致。

**潜在风险（执行时注意）：**
- Task 3 改 LogsReader 签名是 breaking change，所有实现 + 调用点必须同步（cmd/core observability 装配处若有直接调 ListAlerts/ListLogs 也需改）。
- Task 5 改 ListAlerts 签名同样 breaking。
- Task 9 sidecar 注入可能使既有 controller 测试（断言容器数/容器名）需更新。
- Task 12 envStore URL 同步需确认 App.vue 已有 onMounted（避免重复 env 加载）。
