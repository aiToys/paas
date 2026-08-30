# 共享配置应用侧引用（shared ns → 应用发现 merge）

日期：2026-08-31
状态：待用户审阅
前置：环境隔离 + 泳道覆盖（0038）、UX 改造 L1+L2（2026-08-30 已上线）

## 背景与问题

共享配置（scope=shared ns）管理端完整（建/编辑/发布/版本），但消费端只有裸 API（ns 级 `/published` + governance:read API Key + 手拼 nsID）。**创建后无产品化通道让应用使用**——用户投入配置成本，产出无人消费，功能闭环缺失。

业界共识（Nacos common.yml / Apollo 公共 ns 关联）：**引用显式 + 平台 merge + 应用可覆盖 + 影响面可见**。

## 设计原则

- **应用零改动**：消费方（chatbot 等）继续按应用名发现 `GET /api/configcenter/apps/{name}/published`，shared 值自动出现在 snapshot——无新 SDK、无新 env。
- **三层 merge 单向覆盖**：`shared 引用（基础层）→ app×env 基线（版本化）→ lane 覆盖`，右者胜。应用自身 key 覆盖 shared（逃生门：应用可压制 shared 的错误默认值）。
- **复用既有模式**：引用关系挂 ns 元数据（同 ServiceID 关联先例）；merge 纯函数（同 MergeSnapshot/OverrideHash 模式）；指纹热更新（同 overrideHash 模式）。
- 不做：shared→shared 引用链（嵌套 merge 复杂度爆炸，YAGNI）、按 env 差异化 shared（一个 shared 全 env 同值，需要差异就复制多份）。

## 数据模型

### 引用关系：`cc_ns_refs` 表（migration 0039）

```sql
CREATE TABLE cc_ns_refs (
  id           TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
  app_ns_id    TEXT NOT NULL,   -- 应用派生 ns（引用方）
  shared_ns_id TEXT NOT NULL,   -- shared ns（被引用方）
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, app_ns_id, shared_ns_id)
);
CREATE INDEX idx_cc_ns_refs_shared ON cc_ns_refs (tenant_id, shared_ns_id); -- 影响面反查
```

- 引用挂在 **app 派生 ns 上**而非 Application 实体：configcenter 不 import application（依赖倒置边界既有），且 env 隔离天然生效（引用属于 ns，各 env 的派生 ns 各自引用——想全 env 引用就三个 ns 各引一次，显式且可控）。
- Repository 六方法：`ListRefs(nsID) / AddRef(nsID, sharedNSID) / DeleteRef(id) / ListRefUsers(sharedNSID)（反查影响面）` + sentinel `ErrRefNotFound/ErrRefExists`。
- **引用校验**：AddRef 前置 shared_ns 存在 + scope=shared + 本租户（跨租户/非 shared 统一 404 不泄漏）；拒绝自引（app ns 引用自身）。

### 发现协议扩展（三层 merge + 指纹）

`GET /api/configcenter/apps/{appName}/published?env=&lane=` 响应组装升级：

```
snapshot = MergeSnapshot3(sharedRefs..., appEnvBase, laneOverrides)
```

- **merge 顺序**：按 refs 创建顺序依次铺 shared 快照（后者覆盖前者——多 shared 引用时按引用顺序，UI 展示顺序一致可预期）→ app×env 基线覆盖 → lane 覆盖覆盖。
- **指纹升级**：`sharedHash` 新字段（FNV-1a(排序 "nsID:version" 串)），shared 内容变更 → sharedHash 变 → 客户端热替换。**version 仍只反映 app 自身版本**（shared 变更不 bump 应用版本——版本号是应用侧发布行为的真源，不被外部污染）。客户端判定：version 或 sharedHash 或 overrideHash 任一变化即热替换。
- shared ns 无 active 发布时该层贡献空集（不报错——shared 可能建了还没发布）。
- 不带 env/lane 的发现请求同样享受 merge（向后兼容：无引用时行为与现在完全一致，sharedHash 省略）。

## 防护三条（共享配置的天然危险对冲）

1. **引用显式 + 审计**：引用建立/解除在应用侧操作（AppDynamicConfigs 配置 tab），记审计 `configcenter_ns_ref_add/remove`（detail: app/shared ns 名）。
2. **发布影响面可见**：shared ns 详情页（ConfigCenter 共享视图）展示「被 N 个应用引用」+ 引用方列表（`ListRefUsers` 反查）；发布确认弹窗带此数字——发布者知道影响面。
3. **应用可覆盖逃生门**：merge 方向固定 shared 为最底层，应用自身 key 永远胜出（UI 在引用区提示「应用自身配置优先于共享值」）。

## REST

挂应用维度 composite（`dynamic-configs` 分发），权限 `application:read/write` + AppGuard + 生产闸门（写引用 = 写操作，同 lane 覆盖款）：

```
GET    /api/applications/{id}/dynamic-configs/shared-refs?envId=    列引用（含 shared ns 名/版本/key 数）
POST   /api/applications/{id}/dynamic-configs/shared-refs?envId=    body {sharedNsId} 建引用
DELETE /api/applications/{id}/dynamic-configs/shared-refs/{refId}?envId=  解除
GET    /api/configcenter/namespaces/{id}/ref-users                  影响面反查（governance:read，shared 管理侧）
```

OpenAPI 登记 4 操作。

## 前端

| 文件 | 改动 |
|---|---|
| `app-tabs/AppDynamicConfigs.vue` | 新「共享配置引用」section：已引用列表（ns 名 + active 版本 + key 数 + 解除）+ 添加弹窗（下拉选租户内 shared ns，空态引导去共享视图创建）；提示「应用自身配置优先」；merge 预览（当前生效 snapshot 已含 shared 值，天然可见） |
| `api/configcenter.ts` | +refs 三方法 + refUsers |
| `ConfigCenter.vue` | shared 视图 ns 列表加「被引用」列；详情加引用方列表 |

## 验收标准

1. 建 shared ns + 发布 → 应用配置 tab 引用 → 按应用名发现 snapshot 含 shared key；应用自身同 key 值胜出。
2. shared 重新发布 → 发现端点 sharedHash 变化（version 不变）→ 客户端热替换（chatbot dogfooding 验证）。
3. 解除引用 → snapshot 不再含 shared key，sharedHash 省略。
4. shared 详情显示被 N 个应用引用；发布确认弹窗带影响面数字。
5. 越权：跨租户 shared ns 引用 404；非 shared scope（app 派生 ns）拒引 400。
6. 生产 env 写引用需 prod:write（developer 403）。
7. 回归：无引用应用发现行为与现在完全一致（响应无 sharedHash 字段）；`go test ./...` + 三套前端 build + k8s e2e。

## 留后续

- 引用级 key 过滤/重映射（只引 shared 的部分 key——现全量引入，YAGNI）
- shared 变更通知（webhook 推引用方，现靠轮询指纹）
- shared 引用的灰度（先在一个 lane 验证 shared 新值——现 lane 覆盖已可手工对冲）
