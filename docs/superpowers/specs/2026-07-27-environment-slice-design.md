# 环境切片设计

> 切片目标：给平台补齐「环境」维度--Environment 实体（生产/测试）+ 给 Workload/Binding 加 EnvID+LaneID，
> 按 (tenant, envID) 软隔离，应用详情环境切换 + 跨应用环境视图。
> 蓝图优先级：环境模型基座（承载后续泳道/发布单/联调自动化）。
> 命名定稿见 `docs/superpowers/specs/2026-07-27-platform-modules-blueprint.md`「环境与联调模型」。

## 范围

**做**：
- `internal/environment/`：`Environment` 领域（type: prod|test + cluster?）+ Repository（租户隔离）+ 内存实现（seed）
- `Workload`/`Binding` 加 `EnvID` + `LaneID`（`LaneID` 默认 `"default"` = 基线，预留不实现路由）
- Workload/Binding Repository 按 `(tenant, envID)` 过滤；基线单例约束 `unique(tenant, app, env, lane=default)`
- REST：`GET /api/environments`、工作负载/绑定按 envID 过滤
- console-user：工作负载视图加环境过滤；应用详情部署 tab 按环境分组；环境视图（环境列表）
- 软隔离：跨租户 not found

**不做（YAGNI/后续切片）**：
- 泳道路由（染色+降级，归服务治理子系统）-- LaneID 字段预留但不实现路由
- Deployment 实体化（归 DevOps，多应用协同发布时引入）
- Release 发布单 / EnvTemplate（归 DevOps/GitOps）
- AppConfig（应用配置 tab，单独小切片）
- Application.Env 字段清理（保留暂不动应用列表，后续切片清理，文档标注废弃）
- 数据隔离（影子库/schema，难点后置）

## 领域模型

### Environment（新增）

```go
type Environment struct {
    ID        string    // env-prod-bj / env-test
    TenantID  string    // ctx 写入
    Name      string    // 生产-北京 / 测试
    Type      string    // prod | test
    Cluster   string    // 物理落点 prod-bj/prod-sh；test 可空
    Desc      string
    CreatedAt time.Time
}

const (
    TypeProd = "prod"
    TypeTest = "test"
)
```

Repository（复用 pkg/tenant 隔离）：
```go
List(ctx) ([]Environment, error)
Get(ctx, id string) (Environment, error)
Create(ctx, e Environment) error
Delete(ctx, id string) error
```

### Workload/Binding 加环境维度

```go
type Workload struct {
    ... 现有字段
    EnvID  string  // 归属环境
    LaneID string  // "default"=基线（单例）；其他=泳道（预留，本期不创建非 default）
}
```
- `LaneID` 默认 `"default"`，本期所有部署都是基线（不创建泳道实例）
- Repository `List(ctx, envID, wtype)` 加 envID 过滤；`Create` 写入 ctx 的 tenant + 校验 envID 归属同租户
- 基线单例：同 (tenant, app, env, lane=default) 唯一（本期 mock 不强制，留约束说明）

Binding 同理加 `EnvID` + `LaneID`。

### Application

保留 `Env` 字段但标注废弃（`Deprecated` 注释），本切片不清理应用列表。应用是逻辑应用，跨环境；环境维度在 Workload/Binding 上。

## REST API

| 方法 路径 | 权限 | 说明 |
|---|---|---|
| GET `/api/environments` | environment:read | 环境列表（按租户） |
| POST `/api/environments` | environment:write | 创建环境 |
| GET `/api/workloads?type=&envID=` | workload:read | 按 envID+type 过滤 |
| GET `/api/applications/{id}/workloads?envID=` | workload:read | 应用下按环境 |
| GET `/api/applications/{id}/bindings?envID=` | binding:read | 应用下绑定按环境 |

权限：`environment:read/write`、`binding:read` 并入 BuiltinRoles（admin/developer 读写，viewer 只读）。

## seed

| 环境 | 类型 | cluster |
|---|---|---|
| env-prod-bj | prod | prod-bj |
| env-prod-sh | prod | prod-sh |
| env-test | test | （空） |

现有 5 个工作负载挂到环境：
- wl-cs-api / wl-rec-svc -> env-test（基线）
- wl-etl-nightly / wl-etl-backfill -> env-prod-sh
- wl-agent-gw -> env-prod-bj

所有 `LaneID="default"`（基线）。

## 前端

- **工作负载视图**（Workloads.vue）：顶部加环境选择器（默认全部/具体环境），`GET /api/workloads?type=&envID=`
- **应用详情部署 tab**：按环境分组展示工作负载（env-test: cs-api / env-prod-sh: ...）
- **环境视图**（新增 /environments）：环境列表（生产-北京/生产-上海/测试），点进看该环境工作负载

## 验收

- 隔离：`sk-acme-admin` `GET /api/environments` 见 acme 环境工作负载；跨租户 not found
- 环境过滤：`GET /api/workloads?envID=env-test` 只返回测试环境工作负载
- 前端：工作负载视图切环境见不同数据；应用详情部署 tab 按环境分组
- `go test -race` 全绿；新增单测覆盖 Environment 隔离、Workload 按 envID 过滤、基线单例约束说明
- `make lint` 0；`gofmt` 干净；前端三套 build 通过

## 架构约束

- 环境是独立一等公民（非应用子节点），应用×环境多对多，交叉点=部署实例（本期 Workload/Binding 直接带 EnvID，不实体化 Deployment）
- 多租户隔离复用 pkg/tenant，按 (tenant, envID) 过滤
- LaneID 预留（default=基线），不实现泳道路由
- Apache 2.0：无新外部依赖
