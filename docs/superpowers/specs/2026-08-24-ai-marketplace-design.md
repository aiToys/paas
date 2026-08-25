# AI 编排广场（Marketplace）设计

日期：2026-08-24。状态：已与用户对齐（方案 A：统一 Marketplace 实体 + fork 安装）。

## 背景与目标

Agent 编排五个子模块（agent/tool/knowledgebase/prompt/skill）目前全部是「租户私有 + 纯 CRUD」，没有能力复用层。对标 Dify Explore / Coze 商店 / Claude Skills 生态，补齐**跨租户共享广场**：

- 任何租户可把 Skill / Prompt / Tool / Agent 整包**发布**到广场（快照，脱敏）。
- 其他租户浏览 / 搜索 / 按分类筛选，**安装 = fork 副本**到自己租户，之后独立演进。
- KB 含业务数据**不进广场**（已定）。

## 核心决策（已确认）

| 决策点 | 结论 |
|---|---|
| 共享范围 | 租户间公开市场 |
| 共享机制 | 安装 = fork 副本（Dify 模式），非引用同步 |
| 进广场实体 | Skill / Prompt / Tool / Agent 整包 |
| 一期范围 | MVP：发布/下架 + 浏览/搜索/分类 + 安装 fork + 安装量计数；评论/评分/版本链留后续 |
| Tool 凭证 | 发布时自动剔除敏感 key，安装者自行补填 |

## 数据模型

### 新表 `marketplace_items`（migration 0033，0001 合并 schema）

```sql
CREATE TABLE marketplace_items (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL,              -- skill | prompt | tool | agent
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  category      TEXT NOT NULL DEFAULT '',   -- 统一分类体系（见下）
  snapshot      JSONB NOT NULL,             -- 脱敏后的完整实体快照（不可变）
  publisher_tenant TEXT NOT NULL,           -- 发布者租户（展示发布者名）
  publisher_name    TEXT NOT NULL DEFAULT '',
  installs      INT NOT NULL DEFAULT 0,     -- 安装计数
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (entity_type, name, publisher_tenant)  -- 同发布者同名唯一，重发布=下架旧+发新
);
```

**快照不可变**：发布即定格，源实体后续修改不影响广场条目；更新 = 下架重发。一期不做版本链。

### 快照结构（`internal/ai/marketplace/model.go`）

```go
type Item struct {
    ID, EntityType, Name, Description, Category string
    Snapshot json.RawMessage          // 按 EntityType 反序列化
    PublisherTenant, PublisherName string
    Installs int
    CreatedAt time.Time
}
// snapshot 内部结构（Agent 整包展开）：
type AgentSnapshot struct {
    Agent agent.Agent                 // 引用字段指向嵌套 fork 后的新 ID（安装时重写）
    Skills []skill.Skill              // Agent 引用的 skill 一并快照
    Prompt *prompt.Prompt             // PromptRef 引用的 active 版本（可空）
    Tools  []tool.Tool                // 引用的 tool（已脱敏）
}
```

安装时：先 fork 嵌套 skills/prompt/tools（生成新 ID），再创建 Agent 并把 `Skills/PromptRef/Tools` 重写为新 ID。

### 既有实体加字段（memory + pg + migration 同步）

- `Skill.Category string` + `Skill.UseCases string`（适用场景）+ `Skill.Examples string`（使用示例，markdown）
- `Prompt.Category string`、`Tool.Category string`、`Agent.Category string`
- 四实体加 `InstalledFrom string`（源头 marketplace item ID，空=自建；前端「来自广场」标记可跳回）

分类枚举（前端常量 + 后端透传不校验死）：`writing 写作 / coding 代码 / data 数据分析 / service 客服 / general 通用`。

## 领域与存储

新建 `internal/ai/marketplace/`（克隆 skill 包结构惯例）：

- `model.go`：Item + EntityType 常量 + Validate + `SanitizeConfig(cfg map[string]string) map[string]string`（剔除 key 名含 `apikey|token|password|secret|authorization` 的项，不区分大小写，与前端 SENSITIVE_KEYS 语义对齐——但后端是真源）。
- `repository.go` + `memory/` + `pg/`：`List(ctx, entityType, category, q)`（平台级，无 tenant 过滤——广场天然公开）、`Get(id)`、`Create`、`Delete(id)`、`IncInstalls(id)`、`ListByPublisher(ctx, tenantID)`（我的发布）。
- 平台级实体（同 maas Vendor 模式），不走 tenant 过滤；发布/下架在 handler 层校验所有权。

## REST API（`internal/ai/marketplace/handler.go`，权限 `agent:read/write`）

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/marketplace?entityType=&category=&q=` | 登录（agent:read） | 广场列表（不含 snapshot 全文，列表返回元信息+描述） |
| `GET /api/marketplace/{id}` | agent:read | 详情（含 snapshot 预览） |
| `POST /api/marketplace` body `{entityType, entityId}` | agent:write | 发布：从本租户实体生成脱敏快照落库 |
| `DELETE /api/marketplace/{id}` | agent:write + 发布者租户 | 下架（已安装副本不受影响） |
| `POST /api/marketplace/{id}/install` | agent:write | 安装 fork 到本租户，计数 +1 |
| `GET /api/marketplace/published` | agent:read | 我的发布列表 |
| `GET /api/admin/marketplace` | super_admin | admin 跨租户管理（下架违规） |

发布语义：EntityId 反查本租户实体（not found 404）→ 校验 Category 非空（引导补全元数据）→ 脱敏 → upsert（同 entityType+name+publisher 唯一，重发布覆盖）。
安装语义：同名冲突自动后缀 `-2/-3`；fork 完成返回新实体 ID；写审计 `marketplace_install`（含 source item ID）。
OpenAPI 登记 7 操作，`{data:T}` 契约。

## 前端（console-user）

- **广场页 `/ai/explore`（Explore.vue）**：顶部维度过滤条（entityType tab + 分类 pill + 搜索框，复用 Observability 多维度范式）→ 卡片网格（名称/类型 tag/分类/描述/安装量/发布者）→ 卡片点开详情抽屉（snapshot 预览：Skill 显 instructions/useCases/examples，Prompt 显模板，Tool 显类型+脱敏 Config +「安装后需补填凭证」hint，Agent 显组装结构树）→「安装」按钮。
- **菜单**：AI 服务分组下加「广场」（Shop 图标，置顶第一位——先逛后建）。
- **五个模块页**：详情/编辑处加「发布到广场」按钮（弹窗补分类+确认脱敏提示，Tool 明示「凭证不会发布」）；列表加「来自广场」标记（InstalledFrom 非空显 tag，点击跳广场详情）。
- **Skills.vue 详情化**：列表点行进详情抽屉（instructions + useCases + examples markdown 渲染），替代纯表格。

## 横切

- **多租户**：marketplace_items 平台级公开（同 maas 模型目录先例）；安装 fork 落本租户后即租户隔离；发布只能发本租户实体（handler 从 ctx tenant 查）。
- **安全**：SanitizeConfig 后端真源；快照 JSONB 不含任何凭证；admin 可下架。
- **审计**：publish/unpublish/install 三动作记审计（复用 identityAuditAdapter 模式）。
- **权限**：浏览/安装 `agent:read/write`；无 prod:write（不绑环境）。

## 不做（YAGNI，留后续）

评论/评分/点赞、版本链与「源头已更新」提示、平台官方认证标、KB 广场、快照导出 YAML、广场推荐算法、安装量防刷。

## 测试

- 单测：SanitizeConfig 剔除敏感 key；发布-安装 roundtrip（含 Agent 整包嵌套 fork + ID 重写 + 同名后缀）；重发布覆盖；下架不影响的已装副本；非发布者下架 403；admin 下架。
- e2e（k8s）：发布 skill → 另一租户浏览+安装 → fork 副本可用（绑定 Agent 试运行）→ 审计落库。
