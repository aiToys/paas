# 工作负载切片设计

> 切片目标：让应用从「资源绑定壳」升级为「有运行态」——应用下挂工作负载（Service/Job/CronJob），
> 跨应用工作负载视图按租户可见，扩缩容/删除端到端。本期 mock（进程内），抽象与 API 一次到位，
> 真实 K8s 编排为下一切片。
> 蓝图优先级 #2。复用已落地的多租户隔离（pkg/tenant + Repository 强制过滤）与 RBAC。

## 范围

**做：**
- `internal/workload/`：领域模型 + Repository（租户隔离）+ 内存实现（seed）
- REST：应用下工作负载 CRUD + 跨应用列表
- 权限：`workload:read` / `workload:write` 并入 builtin roles
- console-user：`Workloads.vue`（按类型 tab 接真实 API）+ 应用详情工作负载分组
- cmd/core 装配：路由 + Require + seed

**不做（YAGNI/下一切片）：**
- K8s CRD 下发与真实编排（本期进程内 mock）
- 日志/事件流、HPA、灰度发布（DevOps 切片）
- 工作负载模板/Helm（DevOps 切片）

## 领域模型

```go
type Workload struct {
    ID        string    `json:"id"`
    TenantID  string    `json:"tenantId,omitempty"`
    AppID     string    `json:"appId"`
    Type      string    `json:"type"`     // service / job / cronjob
    Name      string    `json:"name"`
    Image     string    `json:"image"`
    Replicas  int       `json:"replicas"` // 期望副本；job=并行度；cronjob=0
    Ready     int       `json:"ready"`    // 就绪副本
    Status    string    `json:"status"`   // running/deploying/failed/succeeded/pending
    Schedule  string    `json:"schedule,omitempty"`  // cronjob 专属
    Command   string    `json:"command,omitempty"`
    CreatedAt time.Time `json:"createdAt"`
}
```

类型与状态常量：
- Type：`TypeService` / `TypeJob` / `TypeCronJob`
- Status：`StatusRunning` / `StatusDeploying` / `StatusFailed` / `StatusSucceeded` / `StatusPending`
- 校验：`Validate()` 检查 type 合法、name/image 非空、cronjob 须有 schedule。

## Repository（租户隔离）

```go
type Repository interface {
    // List 按 appID 过滤；appID 空串表示跨应用（仍按租户）；可选 type 过滤。
    List(ctx, appID, wtype string) ([]Workload, error)
    Get(ctx, id string) (Workload, error)
    Create(ctx, w Workload) error
    // Update 调整副本与状态（扩缩容/暂停/恢复）。
    Update(ctx, id string, replicas int, status string) (Workload, error)
    Delete(ctx, id string) error
}
```

实现内部 `tenant.TenantFrom(ctx)` 强制过滤，缺失即拒；跨租户访问统一 not found。`Create` 的 TenantID 从 ctx 写入，校验 appID 归属同租户（应用存在且同租户）。

## REST API

| 方法 路径 | 权限 | 说明 |
|---|---|---|
| GET `/api/applications/{id}/workloads` | workload:read | 应用下工作负载 |
| POST `/api/applications/{id}/workloads` | workload:write | 创建 |
| GET `/api/workloads?type=service` | workload:read | 跨应用列表（租户+类型） |
| PUT `/api/workloads/{id}` | workload:write | 扩缩容/状态 |
| DELETE `/api/workloads/{id}` | workload:write | 删除 |

handler 复用 application 的 Authorize 注入模式（`RequestAllowed`）。

## 权限

`identity.BuiltinRoles` 增 `workload:read`（admin/developer/viewer）/ `workload:write`（admin/developer）。

## seed

| 应用 | 工作负载 |
|---|---|
| app-cs (acme) | service `cs-api` img `paas/qwen-cs:7b` 2/2 running |
| app-rec (acme) | service `rec-svc` img `paas/rec:latest` 3/4 deploying |
| app-etl (globex) | cronjob `etl-nightly` `0 2 * * *` img `paas/etl:1.2` succeeded |
| app-etl (globex) | job `etl-backfill` img `paas/etl:1.2` running |
| app-agent (globex) | service `agent-gw` img `paas/agent:0.9` 2/2 running |

## 前端

- `Workloads.vue`：跨应用列表，顶部三 tab（服务/Job/CronJob），接 `GET /api/workloads?type=`；行展示 名称/应用/镜像/副本(ready/replicas)/状态/调度(cronjob)；扩缩容按钮（PUT）+ 删除。
- router：`/workloads/services|jobs|cronjobs` 指向 `Workloads.vue`，props 传 type。
- 应用详情：新增「工作负载」分组，接 `GET /api/applications/{id}/workloads`。

## 验收

- 隔离：`sk-acme-admin` `GET /api/workloads?type=service` 只见 acme 的 service；globex 同理。
- CRUD：创建 → 列表可见；PUT 改副本 → ready 变化；DELETE → 消失。跨租户 PUT/DELETE → 404。
- 前端：三 tab 真实数据；切租户可见隔离；应用详情见工作负载分组。
- `go test -race` 全绿；新增单测覆盖 Validate、Repository 隔离、handler CRUD。
- `make lint` 0；`gofmt` 干净；前端三套 build 通过。

## 架构约束

- 工作负载是应用运行形态，归属应用（appID），不是可绑定资源——不进 ResourceCount。
- 多租户隔离复用 Core 治理（pkg/tenant），插件/领域不得绕过。
- 本期不下发 K8s；Repository 接口已为未来 controller-runtime 编排铺路（期望状态 vs 就绪状态分离）。
- Apache 2.0：无新外部依赖。
