# Code Review（PR 评审闭环）设计

日期：2026-08-25
状态：已确认

## 背景与目标

平台代码库能力已有「Git 提交记录 + 文件树浏览 + 构建联动」（内置 Gitea 无头后端），缺 Code Review 闭环。本设计基于内置 Gitea PR API 落地**轻量评审**：PR 列表 → diff 查看 → 整体评审（approve/request-changes/comment）→ merge。

对标 Gitea 简版 MR 流；行级评论、多轮评审状态机留后续（Gitea API 天然支持增量加装，不锁死架构）。

## 需求决策（已确认）

1. **轻量评审（档位 A）**：PR 列表 + 文件级 unified diff + 整体 approve/request-changes/评论 + merge。无行级评论。
2. **PR 独立成体**：PR 是应用代码仓库的一等公民，与变更管理（Change/IntegrationBatch 火车模型）并行互不干扰。集成批次的自动 merge 机制照旧。评审门禁挂钩留后续。
3. **权限对齐应用级权限**：查看 = 读不拦；评论/approve = `write`（developer+）；**merge = `release`**（maintainer+，与发布/回滚/审批同档）。未开受限应用走租户 RBAC（现状语义）。
4. **双入口 + 仅 internal 仓库**：应用详情「代码仓库」tab + DevOps 中心「评审」tab 跨应用聚合。external 仓库平台无凭证，不展示 PR。

## 架构

**PR 真源永远是 Gitea，平台不落库、无新实体、无 migration。** 控制面只做 API 适配 + 权限 + 展示（与「控制面只管元数据」解耦原则一致，评审记录天然在 Gitea 内，零同步成本）。

```
console-user 代码仓库 tab / DevOps 评审 tab
  → devops handler（composite /pulls 分发 + AppGuard + 审计）
  → gitea.Client（PR API 适配，basic auth 既有体系）
  → 内置 Gitea
```

## 后端设计

### Gitea client 扩展（internal/devops/gitea/client.go）

全部走既有 `doJSON`/basic-auth 体系：

- `ListPRs(ctx, owner, repo, state)` → `GET /repos/{o}/{r}/pulls?state=open|closed|all`，返回 `[]PullRequest`（number/title/head/base/user/body/created/merged/mergeable）。
- `GetPR(ctx, owner, repo, number)` → 单个 PR 详情。
- `GetPRDiff(ctx, owner, repo, number)` → `GET /repos/{o}/{r}/pulls/{n}.diff`，返回原始 unified diff 文本。**后端只透传文本，前端解析渲染**（不建 diff 解析领域模型，KISS）。
- `CreatePR / ReviewPR`（`POST /pulls/{n}/reviews`，`Do: "APPROVE"|"REQUEST_CHANGES"|"COMMENT"` + body）。
- `MergePR(owner, repo, number)` → 复用既有 `doMerge` 路径（与 change 集成 merge 同源，DRY）。

### REST 端点

挂 devops handler，`/api/applications/{id}/repositories/{rid}/pulls`（composite 按路径分发）：

| 端点 | 说明 | 权限 |
|------|------|------|
| `GET .../pulls?state=` | PR 列表 | repository:read（读不拦 Guard） |
| `GET .../pulls/{number}` | 详情 + diff 文本 | 同上 |
| `POST .../pulls/{number}/reviews` | 评审三态 | repository:write + AppGuard `write` |
| `POST .../pulls/{number}/merge` | 合并 | repository:write + AppGuard `release` |

横切细则：

- 仅 internal 仓库（external 返 405，与 tree/commits/file 同语义）。
- diff 响应 `{data:T}` 包裹；体积 >2MB 截断 + 提示字段（防御大 PR）。
- Gitea merge 422（冲突/不可合并）→ 409 中文错误。
- OpenAPI Operation 登记 4 操作。
- 评审与 merge 记审计：`pull_request_review` / `pull_request_merge`（actor/resource/target 经 identityAuditAdapter 模式）。
- 跨应用聚合（DevOps 评审 tab）复用既有跨应用列表模式：`GET /api/pulls`（租户内全部 internal 仓库的 open PR 聚合，repository:read）。

### 前端设计

- **代码仓库 tab**：internal 仓库卡片加「评审」入口 → PR 列表（state tab：开放/已合并/已关闭）。
- **PR 详情页** `/devops/pulls/:repoId/:number`（新 `PullDetail.vue`，对齐 ChangeDetail 单据模式）：
  - meta 区：标题/分支 head→base/作者/时间/状态。
  - diff 渲染区：轻量自研解析（按 `diff --git` 分文件，+绿/−红行着色，monospace，文件可折叠）。不引外部 diff 库。
  - 评审操作条：评论框 + 批准/要求修改/仅评论。
  - merge 按钮：AppGuard 403 兜底 + `confirmDangerous`（目标 base 为 main 即主干，一律走危险确认）。
- **DevOps 中心**：新「评审」tab，跨应用聚合 open PR（应用名/标题/分支/作者/时间，点击进详情），并入值班台「等评审」信号（open PR 且非本人提交 → 待处理列）。

## 测试

- gitea client：httptest 模拟 Gitea API（list/get/diff/review/merge，含 422→409 映射），与既有 client 测试同模式。
- handler：权限矩阵（viewer 评论 403 / developer merge 403 / maintainer merge 通过 / 未受限应用放行）+ external 仓库 405 + diff 截断。
- 前端：diff 解析纯函数单测（分文件/行着色/二进制文件 fallback）。

## 明确不做（YAGNI）

- 行级评论、多轮 review summary、CI 状态挂 PR。
- PR 创建端点（用户本地 push 后经变更管理或 Gitea API 建 PR；本期只读+评审+merge）。
- external 仓库 PR（平台无凭证）。
- 评审门禁接入变更管理火车模型（Change 须 PR 通过才能入批）——留后续增强。
