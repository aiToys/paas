# 配置中心环境隔离 + 泳道覆盖设计

**日期**：2026-08-30
**状态**：已确认（用户确认收敛版方向）
**前置**：`2026-08-29-configcenter-app-centric-design.md`（应用维度改造，已落地）

## 背景与问题

配置中心应用维度改造后遗留两个对标业界的缺口（用户连续指出）：

1. **不区分环境**：同一应用的动态配置对 test/prod 同时生效——测试环境改 `recommend_topk` 验证，发布后生产立刻跟着变。与平台「生产写需 `prod:write`」横切防护不一致（appconfig/workload/dataservice 全有环境闸门，配置中心是唯一漏网）。
2. **无泳道维度**：feature-x 泳道的服务想改配置做联调实验，改的是全租户共享那份，基线和其它泳道立刻被污染——泳道「隔离变更」的价值在配置上失效。而平台 lane 已是一等实体（workload 带 LaneID、LaneGC 回收、染色 SDK），配置中心是断链的。

## 设计原则（收敛版，砍掉过度设计）

- **merge 链只做两层**：`app×env（版本化基线）→ lane（key 级覆盖）`。shared scope **不参与 merge**（独立 ns 独立拉，维持现状——实际使用率近零，为其建 merge 层违反 YAGNI）。
- **lane 覆盖无版本链**：泳道覆盖是「当前生效的 key 差异集」，编辑即生效、随泳道回收消失。不叫发布/回滚（UI 文案：「泳道覆盖（即时生效）」）。理由：泳道本身是临时实体，为其建不可变快照链是伪需求；且避免「回滚上层连带改变泳道视图」的语义坑。
- **服务端 merge，客户端零感知**：发现端点返回 merge 结果，客户端不实现分层逻辑。
- **lane 来源 = 部署注入，非请求染色**：配置拉取的 lane 一律取 `PAAS_LANE_ID` env（SDK 既有约定，`sdk/paas-registry/lane.go`）。染色是请求路径语义，配置是部署路径语义，不混用。

## 数据模型

### 1. Namespace 加 EnvID（migration）

```
cc_namespaces: + env_id TEXT NOT NULL DEFAULT ''   -- '' = 无环境归属（存量数据/共享 ns）
唯一约束：(tenant_id, app_id, env_id) —— app scope 按 (app, env) 派生
```

- app scope 懒建从 `EnsureByApp(ctx, appID)` 变 `EnsureByAppEnv(ctx, appID, envID)`，ns 名 `app-<appID>-<envID>`（envID 空时保持 `app-<appID>` 兼容）。
- 同一应用在 test/prod 各一份独立配置、独立版本历史、独立发布/回滚。
- shared scope 的 ns env_id 恒空（不参与环境模型）。
- **存量迁移**：已有 app ns 的 env_id 置 `''`，语义 = 「不区分环境的默认配置」——发现端点 env 参数不匹配任何 (app,env) ns 时回退到 env_id='' 的 ns（向后兼容：chatbot 等已接入客户端升级平台后行为不变）。

### 2. 泳道覆盖（新实体，挂 configcenter 包）

```go
// LaneOverride：某 (tenant, app, env, lane) 的 key 级配置覆盖。
type LaneOverride struct {
    TenantID string
    AppID    string
    EnvID    string
    LaneID   string   // 泳道名（非 default）
    Key      string
    Value    string
    UpdatedAt time.Time
}
// 唯一约束 (tenant_id, app_id, env_id, lane_id, key)
```

- 存储新表 `cc_lane_overrides`（migration 同批）。
- 常规 CRUD（upsert/delete/list by app×env×lane），无版本/发布语义。
- **泳道回收联动**：LaneGC / ReclaimLane 回收泳道时级联删该 (app, env, lane) 的覆盖（依赖倒置接口注入，模式同 appCascadeDeleter）。删除是物理删——泳道已消失，覆盖无保留价值。

## 发现协议（merge 真源）

```
GET /api/configcenter/apps/{appName}/published?env=<envID>&lane=<laneName>
```

解析顺序：
1. 按 (app, env) 找 active publish 快照；找不到回退 env_id='' 的 ns；再找不到 → base = {}。
2. lane 非空且非 default 时，取该 (app, env, lane) 的 LaneOverride 覆盖 base 对应 key；lane 的 env 匹配规则同上（先精确 env，回退 ''）。
3. 返回 `{"published":bool, "version":N, "snapshot":merged}`。

- version 取 base（app×env 层）的 active version；纯 lane 覆盖变化不改 version（客户端轮询比对 version 会漏掉 lane 变化——**发现响应加 `overrideHash`**：lane 覆盖集的 hash，客户端 version 或 overrideHash 任一变化即热替换）。
- 不带 env/lane 参数：行为与现在完全一致（`env=''` + `lane=''`），零破坏。
- 三路不泄漏原则保持：未知应用/无 ns/无 active 统一 `{"published":false}`。

## REST 变更

### 应用维度（挂 application composite，权限不变）

- `GET/POST /api/applications/{id}/dynamic-configs?envId=`（envId 必传或取默认环境；懒建按 (app, env)）
- `DELETE .../items/{itemId}?envId=`（归属校验含 env）
- `POST .../publish?envId=`（**目标 env 属 prod 时需 `prod:write`**，接 EnvTypeResolver 横切）
- `GET .../publishes|published?envId=`
- **新增泳道覆盖**：`GET/PUT/DELETE /api/applications/{id}/dynamic-configs/lane-overrides?envId=&lane=`（PUT body `{key,value}` upsert 语义，DELETE `?key=`）。写权限同 application:write + AppGuard write；prod env 同样接 prod:write。

### 客户端发现

- 上面的 merge 端点（公开契约不变，加可选 query 参数）。

## 生产闸门（横切继承）

- app×env ns 的写/发布/回滚：目标 env type=prod → `prod:write`（EnvTypeResolver，与 appconfig 同款）。环境类型解析失败 fail-closed 按生产。
- lane 覆盖写：同上接 prod:write。泳道覆盖高频场景是非生产联调，生产泳道（灰度验证）改配置需管理员——合理。

## 前端

- **AppDynamicConfigs.vue**：读顶栏环境 store（与 appconfig 同款交互），envId 贯穿全部请求；页面加「泳道覆盖」子区（顶栏环境 + 泳道下拉（取 `/api/lanes` 该环境泳道列表），KV 差异集表格 + 增删，标注「即时生效，随泳道回收消失」）。
- **ConfigCenter.vue 共享视图**：不动（shared 无环境模型）。
- **发布确认**：目标 prod 时走 useDangerConfirm 生产档（isProd 显式传入，与 DevOps 发布同款）。

## 客户端（dogfooding）

- chatbot dynconfig：env 取部署注入 `PAAS_CONFIG_ENV`（controller 已给 service 类型 Pod 注入 env 相关变量，缺的补），lane 取 `PAAS_LANE_ID`（已有约定）。发现 URL 带 `?env=&lane=`，热替换比对 `version + overrideHash`。
- 泳道部署的 chatbot 实例（如 integration 分支泳道）自动拉到泳道覆盖后的配置——联调实验闭环。

## 不做（YAGNI 记录）

- shared 参与三层 merge、配置继承链 UI 可视化——等真实诉求。
- lane 覆盖版本化/回滚——泳道临时实体，伪需求。
- 按比例灰度下发（一半实例见新值）——那是发布策略不是配置语义。
- 长连接 push——轮询 + hash 比对已够 dogfooding 规模。
- 覆盖 value 的类型字段——text 单类型够（复杂结构放 value 里 JSON 字符串）。

## 验收

1. 同应用 test/prod 各建配置独立发布，互不可见；生产发布被 developer 角色拒（403 prod:write）。
2. feature 泳道覆盖 `recommend_topk=5`，基线配置不变；带 `lane=feature` 发现返回 5，不带返回基线值。
3. 泳道回收后覆盖消失，发现回落基线。
4. 不带 env/lane 的旧调用行为与升级前一致（chatbot 不改代码也能跑，只是拿默认 env 配置）。
5. 存量 ns（env_id=''）继续可发现。
