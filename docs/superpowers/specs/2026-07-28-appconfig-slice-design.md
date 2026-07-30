# 应用配置切片设计

> 切片目标：应用详情收尾「配置」tab --env/Secret 键值，工作负载级静态配置，启动注入。
> 与配置中心严格区分（CLAUDE.md 已定）：本切片是工作负载级静态配置（改了重启），配置中心是运行时动态、跨实例、版本灰度（归服务治理）。
> 生产安全横切框架二次验证：生产改 Secret 受 `prod:write` + 前端 `useDangerConfirm`。
> 约束：进程内 mock（不真注入工作负载），接口为未来接 K8s ConfigMap/Secret 铺路。

## 领域模型

包 `internal/appconfig/`（避开 Go `config` 通用词），复用 `pkg/tenant` 隔离。

```go
// ConfigItem 是应用在某环境的单个配置项（env 或 Secret）。
type ConfigItem struct {
    ID        string    // cfg-xxx
    TenantID  string    // ctx 写入
    AppID     string
    EnvID     string    // 归属环境（配置是环境内）
    Key       string    // 键，如 LOG_LEVEL / DB_PASSWORD
    Value     string    // env 明文；secret 后端明文存储，API 返回掩码
    Type      string    // env | secret
    UpdatedAt time.Time
}
```

**Secret 掩码**：Repository.List 返回前对 `type=secret` 的 Value 替换为固定掩码 `"••••••"`（不泄漏长度/内容）。明文仅存储，不回显（mock 语义；真实场景接 K8s Secret 加密存储）。

Repository：
```go
type Repository interface {
    List(ctx, appID, envID string) ([]ConfigItem, error) // secret 值掩码
    Upsert(ctx, item ConfigItem) (ConfigItem, error)     // 同 (app,env,key) 更新否则插入
    Delete(ctx, id string) error
}
```

## REST API

| 方法 路径 | 权限 | 说明 |
|---|---|---|
| GET `/api/applications/{id}/configs?envId=` | config:read | 配置列表（Secret 掩码） |
| POST `/api/applications/{id}/configs` | config:write + prod:write(若prod) | 新增/更新配置项 |
| DELETE `/api/applications/{id}/configs/{cfgId}` | config:write + prod:write(若prod) | 删除 |

权限 `config:read/write` 并入 BuiltinRoles（admin/dev 读写，viewer 只读）。生产环境写操作注入 EnvTypeResolver 校验 `prod:write`（developer 生产只读）。

## 前端

应用详情新增「配置」tab（`app-tabs/AppConfigs.vue`）：
- 依赖顶栏 scope：scope 具体环境显示该环境配置；scope 全部提示「请在顶栏选择具体环境」（配置是环境内，不在 tab 内再造环境切换）
- 类型切换：环境变量 / Secret（两个 el-table 或一个带类型列）
- 列表：key / value（Secret 掩码）/ 类型 / 更新时间
- 增/改：el-dialog（key/value/type）；Secret 操作 + 生产环境走 `useDangerConfirm`（输入名称确认）
- 删：生产走 `useDangerConfirm`

## seed

acme app-cs env-acme-test：LOG_LEVEL=info（env）、API_KEY=sk-*** （secret）。
globex app-agent env-globex-prod：MODEL_TIMEOUT=30（env）。

## 不做（YAGNI/后续）

- 配置中心（运行时动态，归服务治理）
- 配置版本/灰度/回滚（归配置中心）
- 真实 K8s ConfigMap/Secret 注入（mock 期记录关联，不真注入）
- 配置导出/导入、批量操作

## 任务分解

| 任务 | 内容 |
|---|---|
| CFG-T1 | `internal/appconfig/` 领域 + Repository + 内存实现（seed）+ 单测 |
| CFG-T2 | handler（REST）+ 权限 + prod:write（EnvTypeResolver）+ 单测 |
| CFG-T3 | cmd/core 装配 + composite 路由加 `/configs` + identity 加 config 权限 |
| CFG-T4 | 前端 AppConfigs.vue + ApplicationDetail 接入「配置」tab |
| CFG-T5 | 文档 + 端到端验证 |

## 验收

- 配置 CRUD：增/改/删/列表（Secret 掩码）
- 隔离：跨租户 not found
- 生产安全：dev 改生产配置 403、admin 200、dev 改测试 200
- 前端：配置 tab 依赖顶栏 scope；Secret 操作生产走危险确认
- go test -race 全绿；lint 0；前端 build 通过
