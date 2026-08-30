# 配置中心 UX 改造 L1+L2：显式环境 + 变更 diff + 泳道灰度发布链路

日期：2026-08-30
状态：待用户审阅
前置：环境隔离 + 泳道覆盖（2026-08-30 已上线）、published 参数名修复（09eeb8c）

## 背景与问题

用户实测反馈（2026-08-30）：
1. 「生效中 v1 但 v3 active」——published 参数名 bug（已修 09eeb8c），但排查过程暴露更深的 UX 缺陷：**生效状态、发布历史、草稿三者的关系靠用户脑补**。
2. 「草稿不见了」——env 隔离上线前的存量草稿留在基线 ns（env=''），切到具体 env 后不可见，用户以为丢了。
3. 「配置没有更新」——发布动作的反馈只有一个 toast，没有变更内容确认。
4. 「泳道配置没看到测试数据」——泳道覆盖隐藏在子区，用户不知道它就是**配置灰度**。

对标结论（调研 2026-08）：Nacos/Apollo 解决的是微服务时代基础体验（env 隔离/版本/回滚/审批），这些我们数据模型已具备、**差的是呈现**；AI 云原生时代的新要求（配置=发布决策、灰度验证、成本/质量反馈闭环）中，「灰度验证」恰好可由**泳道覆盖**产品化承载——这是 PaaS 已有但未讲出的故事。

## 设计原则

- **数据模型零改动**（Namespace/Publish/LaneOverride 全部复用），L1 纯前端 + 2 个只读端点，L2 加 1 个提升端点。
- 不引入新概念：env tab 用既有环境实体，灰度用既有泳道语义，提升=promote 一词贯穿 DevOps 已有认知。
- 存量兼容：基线（env=''）草稿提供显式入口而非自动迁移（自动迁移有猜测风险——不知道该归 test 还是 prod）。

## L1：显式环境 + 变更可见性（对标 Nacos 及格线）

### 1.1 页内显式 env tab（替代顶栏 scope 隐式跟随）

`AppDynamicConfigs.vue` 顶部加 env 切换条：

```
[ 基线 ] [ env-acme-test ] [ env-acme-prod-bj ]          ＋ 新建环境 →
```

- 数据源：既有 `GET /api/environments`（租户内全部环境）。
- **基线 tab 固定第一个**：承载 env 隔离上线前的存量草稿（解决「草稿不见了」——不是迁移，是给基线一个显式的家）。
- 当前选中 env 高亮；tab 徽标显示该 env 的 active 版本号（如 `v2`，未发布显「未发布」灰字）。
- 与顶栏 scope 解耦：本页 env 选择是**页面局部状态**（query 参数 `?env=`），不写回全局 envStore（配置查看是读操作，不应改全局操作面 scope）。初值取 query > 顶栏 scope > 基线。
- prod env tab 带红色标识（与全局生产视觉语言一致）。

### 1.2 草稿 vs 生效 对比（未发布变更高亮）

配置项列表从「平铺 KV」升级为三态展示：

| 列 | 说明 |
|---|---|
| Key | |
| 生效值 | active publish snapshot 中的值（截断展示，hover 全文） |
| 草稿值 | draft item 当前值；与生效值**不同**时高亮（黄底 + 「已修改」tag）；新增 key 显「新增」绿 tag |
| 操作 | 编辑/删除 |

- 数据源：既有 `fetchAppDynamicConfigs`（draft）+ `fetchAppPublished`（snapshot），前端 diff——**不加后端端点**（数据都在手里，diff 是纯展示逻辑）。
- 顶部状态条：`生效中 v2 · 3 项待发布（2 修改 1 新增）· [发布] [回滚]`。
- 未发布的 key（只在 draft 无生效值）在发布确认中明确标注「将新增」。

### 1.3 发布确认弹窗带 diff

点「发布」→ el-dialog 确认：

```
即将发布 env-acme-test 的 3 项变更（v2 → v3）：

  修改  env_check      test-val → test-val-v2
  修改  text.yaml      (内容变更，点击展开)
  新增  new_key        first-value

  [展开完整 diff]
```

- 纯前端计算（draft vs active snapshot），复用 1.2 的 diff 结果。
- prod env 发布走 `confirmDangerous`（输入环境名确认，与既有生产闸门语言一致）。

### 1.4 后端新增：`GET /dynamic-configs/diff`（可选，一期先不做）

前端 diff 已够用；此端点留给 SDK/CLI 消费者，二期再补。（YAGNI）

## L2：泳道灰度发布链路（把 lane override 产品化）

核心叙事：**改配置 → 先发泳道验证 → 提升到基线**。复用 DevOps 已建立的 promote 认知。

### 2.1 发布入口二选一

发布确认弹窗的发布目标从「直接生效」扩展为：

```
发布到： (●) 基线（全量生效）  ( ) 泳道 [feature-x ▾]（仅该泳道实例生效）

选择泳道时：变更以 key 级覆盖写入（即时生效、无版本记录、随泳道回收消失）
```

- 泳道下拉数据源：既有 `GET /api/lanes?envId=`（只列 active）。
- **选泳道 = 逐项调既有 `upsertLaneOverride`**（前端循环，body 为 diff 中变更的 key/value）——不加新端点。
- 不选泳道 = 既有 publish（新版本）。

### 2.2 泳道覆盖子区升级为「灰度验证」视图

现有泳道覆盖子区改造：

```
┌─ 灰度验证 ────────────────────────────────┐
│ 泳道: [feature-x ▾]   状态: active        │
│                                            │
│ 覆盖项（2）：           vs 基线：           │
│   env_check = v2-draft   (基线: test-val)  │
│   new_key    = on        (基线: 无·新增)   │
│                                            │
│ 发现地址验证：                              │
│   GET /published?env=...&lane=feature-x    │
│   → 合并结果预览（服务端真实 merge）        │
│                                            │
│ [提升到基线]  [放弃覆盖]                    │
└────────────────────────────────────────────┘
```

- **覆盖项 vs 基线对照**：每项显示覆盖值与基线值 diff（数据都在前端）。
- **合并结果预览**：调既有 `fetchAppPublished({envId, lane})`——服务端 MergeSnapshot 真实结果，用户看到「泳道实例实际拿到什么」。
- **提升到基线**：新端点（见 2.3）。
- **放弃覆盖**：逐项调既有 `deleteLaneOverride`。

### 2.3 新端点：`POST /dynamic-configs/lane-overrides/promote`

唯一的新后端能力。语义：把泳道覆盖**合并进基线草稿并发布新版本**，然后删除该泳道这些 key 的覆盖。

```
POST /api/applications/{id}/dynamic-configs/lane-overrides/promote?envId=&lane=
```

流程（服务端单事务语义，顺序执行）：
1. 权限：application:write + AppGuard write + **prod 闸门**（env type=prod 需 prod:write——提升即全量生效，与 publish 同级危险）。
2. `ListLaneOverrides(appID, envID, lane)` 取覆盖集；空集 400「无覆盖可提升」。
3. 逐 key `UpsertItem` 进基线 draft（覆盖值写入）。
4. `CreatePublish`（快照含覆盖值，产生新版本）。
5. 逐 key 删除该 lane 的覆盖（`DeleteLaneOverride`）。
6. 审计 `configcenter_lane_promote`（detail: lane/version/key 数）。

失败语义：步骤 3/4 失败时不删覆盖（提升失败=泳道维持原状，可重试）；步骤 5 失败则新版本已生效但覆盖残留（幂等重试 promote 会 400 空集——此时覆盖已无意义，提示用户手动清理；接受此边界，不做分布式事务）。

### 2.4 明确不做（本期）

- 按流量百分比的配置分桶（泳道语义已覆盖，Nacos 也只是 IP 灰度）。
- 覆盖的版本化（lane override 天生即时生效随泳道消失，版本化违背其存在意义）。
- 配置变更 webhook 通知（留 L3 与反馈闭环一起设计）。
- 长连接 watch 推送（60s 轮询 + hash 比对够用）。

## 前端涉及文件

| 文件 | 改动 |
|---|---|
| `app-tabs/AppDynamicConfigs.vue` | 主改造：env tab 条、三态列表、发布 diff 弹窗、灰度验证视图 |
| `api/configcenter.ts` | +promoteLaneOverrides；fetchAppPublished 已修 envId |
| `api/environment.ts` | 复用既有 listEnvironments（若无此函数则补） |

## 验收标准

1. 切 env tab，各 env 的草稿/历史/生效版本独立正确；基线 tab 可见存量草稿。
2. 修改 2 项 + 新增 1 项后，列表高亮「2 修改 1 新增」；发布弹窗显示准确 diff；发布后 v+1 且高亮清零。
3. 选泳道发布 → 泳道实例经 `/published?lane=` 拿到覆盖值，基线不变；「提升到基线」后基线新版本含覆盖值且泳道覆盖清空。
4. prod env 发布/提升均需输入环境名确认（developer 403）。
5. 既有回归：不带 env 的发现协议、chatbot 60s 轮询热更新不受影响。
6. `go test ./...` + 三套前端 build + k8s e2e（含 chatbot dogfooding 泳道覆盖场景）。

## 留后续（L3 预告）

AI 行为配置档案（profile 类型：prompt+模型路由+参数+工具集整体版本化）、publish 联动 EvalRun 评估、token 成本 join 视图——单独 spec。
