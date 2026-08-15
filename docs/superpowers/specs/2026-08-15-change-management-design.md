# 变更管理（Change / IntegrationBatch）设计

> 日期：2026-08-15
> 状态：待用户审阅
> 层级：架构级（新实体 + 状态机 + Gitea 编排 + 流水线联动 + 前端新页面）

## 1. 背景与问题

当前 DevOps 缺少「新建变更」的能力。用户的原始诉求：

> 变更就是创建一个 feat 或 hotfix，在 git 上创建对应分支。创建变更后用户可以直接拉取创建好的 git 分支，也可以使用已有分支创建变更。变更创建后多个变更可以合在一起进行测试（集成测试）。流水线的节点就是多个变更合成一个临时测试分支进行测试，测试成功后可以将多个变更同时上线。

现状缺口：

- **变更无实体**：开发者改代码只能直接 push 到任意分支，平台不感知「一个变更单元」的存在，无法追踪「这个 feat 从哪来、测过没有、上没上线」。
- **分支创建不在平台内**：建分支要跳出平台去 git 操作，平台只被动接收 webhook。
- **无集成测试抽象**：多个变更合在一起测试 = 手动 merge 到某个分支再触发流水线，无平台级记录与编排。
- **上线粒度是镜像不是变更集**：Release 记录镜像不记录「这批上线包含了哪几个变更」。

## 2. 核心模型：火车发车模型

借鉴发布火车（Release Train）：变更像车厢，集成批次像火车，测试通过 = 火车可以发车（上线），整批变更一次到达生产。

```
Change(feat-a) ─┐
Change(feat-b) ─┼─→ IntegrationBatch(集成分支 integration/20260815-1) ─→ CI 流水线(泳道隔离)
Change(hotfix-c)┘         │
                          ├─ 测试通过 → approved → 上线：变更逐个 merge 回 main → CD 流水线 → released
                          └─ 测试失败 → failed → 定位冲突/失败变更 → 移出/修复 → 重新集成
```

三层实体：

| 实体 | 职责 | 类比 |
|------|------|------|
| **Change** | 一个变更单元 = 一条 git 分支 + 元信息（标题/类型/状态） | 车厢 |
| **IntegrationBatch** | 一批变更的集成测试容器 = 临时集成分支 + 关联 CI run | 火车 |
| **Release（既有）** | 上线动作，扩展记录批次来源 | 发车 |

## 3. 实体设计

### 3.1 Change

```go
// internal/devops/change/model.go
type Change struct {
    ID        string    // chg-<rand>
    TenantID  string
    AppID     string
    RepoID    string    // 关联 CodeRepo（internal 仓库，变更管理的载体）
    Title     string    // 人类可读标题
    Type      string    // feat | hotfix
    Branch    string    // git 分支名（feat/xxx 或 hotfix/xxx，自动派生或用户指定已有分支）
    BranchCreated bool  // 平台创建的分支（true）vs 引用已有分支（false）
    BaseBranch string   // 从哪个分支切出，默认 main
    Status    string    // 状态机见 3.2
    BatchID   string    // 当前所属集成批次（空=未入批次）
    CreatedBy string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

分支命名约定：平台创建分支时 `Type/Slug`（如 `feat/user-export`），Slug 从标题拼音/ascii 派生或用户填写。**允许引用已有分支**（`BranchCreated=false`）：用户在外部 git 已有分支，只注册变更元信息——此时平台不建分支，删除变更也不删分支。

### 3.2 Change 状态机

```
open ──(加入批次→集成开始)──→ integrated ──(批次测试通过)──→ tested
  │                                │                          │
  │                                └──(批次失败/被移出)──→ open（可重新入批）
  │                                                           │
  ├──(放弃)──→ abandoned                                      ├──(批次上线)──→ released
  └──(上线后回退)──→ reverted ←──────────────────────────────┘
```

- `open`：已创建，可拉取开发 / 可加入批次
- `integrated`：所在批次正在集成测试
- `tested`：所在批次测试通过，等待上线
- `released`：所在批次已上线
- `reverted`：上线后被回退（预留，本期仅在回滚 Release 时 best-effort 标记）
- `abandoned`：主动放弃（分支保留在 git，仅平台状态终结）

状态由批次生命周期驱动，**Change 自身无独立操作改变状态**（除 abandoned）——单一事实源是批次。

### 3.3 IntegrationBatch

```go
type IntegrationBatch struct {
    ID          string    // batch-<rand>
    TenantID    string
    AppID       string
    RepoID      string
    Title       string    // 如「8月15日集成」
    Branch      string    // 临时集成分支 integration/<date>-<seq>
    Status      string    // 状态机见 3.4
    ChangeIDs   []string  // 批内变更（有序，merge 顺序）
    PipelineID  string    // 使用的 CI 流水线
    RunID       string    // 集成测试 run（批次触发的 CI run）
    ReleaseIDs  []string  // 上线产生的 Release（上线动作的落地记录）
    CreatedBy   string
    CreatedAt   time.Time
    FinishedAt  time.Time
}
```

集成分支命名：`integration/20260815-1`（日期 + 当日序号）。分支是**临时**的：批次终态（released/failed/abandoned）后保留供排查，不主动删除（YAGNI，git 分支便宜）。

### 3.4 IntegrationBatch 状态机

```
collecting ──(触发集成测试)──→ building ──→ testing ──→ tested ──(批准上线)──→ releasing ──→ released
    │                            │            │
    │                            └──(merge 冲突)──→ conflict
    │                                         └──(CI 失败)──→ failed
    ├──(放弃)──→ abandoned
    └─ conflict/failed ──(修复后重新触发)──→ building（循环）
```

- `collecting`：收集变更中（add/remove change）
- `building`：正在合并变更分支 → 集成分支（同步编排，见 5.2）
- `conflict`：合并冲突，标记冲突的变更（见 5.2 冲突处理）
- `testing`：CI run 正在跑（集成分支部署到测试泳道 + 测试）
- `tested`：CI 成功，等待人工批准上线
- `releasing`：正在上线（变更逐个 merge 回 main + CD run）
- `released`：上线完成（终态）
- `failed`：CI 失败（终态可循环：修复后重新触发）
- `abandoned`：放弃（终态）

## 4. REST API

### 4.1 Change 端点

```
GET    /api/applications/{id}/changes?status=          列变更（按状态过滤）
POST   /api/applications/{id}/changes                  创建变更
GET    /api/applications/{id}/changes/{cid}            变更详情
DELETE /api/applications/{id}/changes/{cid}            放弃变更（→abandoned，不删 git 分支）
```

创建 body：

```json
{ "title": "用户导出", "type": "feat", "createBranch": true, "branch": "feat/user-export", "baseBranch": "main" }
```

- `createBranch=true`：平台调 Gitea 建分支（`BranchCreated=true`）
- `createBranch=false` + `branch` 非空：引用已有分支（校验分支存在，Gitea GetBranch）
- 权限：复用 `pipeline:write`（变更管理是 DevOps 域）；跨租户 not found 不泄漏

### 4.2 IntegrationBatch 端点

```
GET    /api/applications/{id}/batches?status=           列批次
POST   /api/applications/{id}/batches                   创建批次（collecting）
GET    /api/applications/{id}/batches/{bid}             批次详情（含变更列表展开）
DELETE /api/applications/{id}/batches/{bid}             放弃批次（→abandoned；仅 collecting）
POST   /api/applications/{id}/batches/{bid}/changes     加入变更 {changeId}
DELETE /api/applications/{id}/batches/{bid}/changes/{cid}  移出变更（仅 collecting/conflict/failed）
POST   /api/applications/{id}/batches/{bid}/integrate   触发集成测试（collecting/conflict/failed → building）
POST   /api/applications/{id}/batches/{bid}/approve     批准上线（tested → releasing；prod:write）
POST   /api/applications/{id}/batches/{bid}/release     执行上线（approve 后自动触发，也可手动重试）
```

`integrate` 是同步编排（Gitea merge 若干次，秒级）+ 异步 CI run；`release` 同理。均记审计（`change:` 前缀 action）。

## 5. 编排流程

### 5.1 创建变更（分支创建）

```
用户 POST /changes (createBranch=true)
  → 校验 app 有 internal CodeRepo（change 管理仅支持 internal 仓库，见 8. 取舍）
  → Gitea CreateBranch(owner, repo, branch, baseBranch)     [新增 client API]
  → Change{Status: open, BranchCreated: true} 落库
  → 返回 CloneURLWithAuth（前端展示 git clone/fetch 命令，用户直接拉取开发）
```

### 5.2 触发集成测试（integrate）

```
POST /batches/{bid}/integrate
  1. 校验批次状态 ∈ {collecting, conflict, failed}，ChangeIDs 非空
  2. 确保集成分支存在（从 main 创建；已存在沿用——重新集成场景）
  3. 逐个 merge：for each change in ChangeIDs（有序）:
       Gitea Merge(head=change.Branch, base=batch.Branch, mode=merge)
       失败(ErrMergeConflict) → 批次标 conflict + 在 Change 上记 conflictWith=前一个变更，
       终止循环，返回 409 + 冲突变更列表
  4. 全部合并成功 → 触发 CI run：
       pipeline = 批次绑定的 CI 流水线（app 第一条 kind=ci 的 pipeline）
       branch = batch.Branch（集成分支）→ run.branch → build stage 构建集成分支；
       deploy stage 的 lane 经既有 {{run.branch}} 占位符解析为集成分支名——分支名唯一即泳道隔离，
       多批次互不干扰（复用 L2 泳道机制，流水线模板零改动）
  5. 批内 Change 全部 → integrated；批次 → testing；记 RunID
  6. CI run 终态回调（轮询发现，见 5.5）→ succeeded: 批次 tested + Change tested
                                          → failed: 批次 failed + Change 回 open（可修后重入）
```

**关键复用**：CI run 的 branch 参数直接用集成分支名，build stage 天然构建合并后的代码；deploy 泳道用 batch ID 作 lane，`{{run.branch}}` 解析机制不动。**流水线引擎零改动**。

冲突语义：Gitea Merge 是 PR merge，`merge conflict` 时该 PR 不会创建成功，集成分支停在合并完前一个变更的状态。conflict 状态下用户可选：移出冲突变更后重新 integrate（集成分支重建或续合并，见 5.6 重建策略）。

### 5.3 批准上线（approve → release）

```
POST /batches/{bid}/approve   (tested → releasing, 需 prod:write)
  → 状态落 releasing（锁定：不可再 add/remove change）

POST /batches/{bid}/release（或 approve 内自动触发）
  1. 逐个 merge：for each change in ChangeIDs:
       Gitea Merge(head=change.Branch, base=main, mode=merge)
       冲突 → 批次回 tested + 409 报告冲突变更（生产合并冲突需人工在 git 解决后重试）
  2. 全部合并 → 触发 CD run：
       pipeline = app 第一条 kind=cd 的 pipeline
       branch = main（CD 构建合并后的主干）
  3. CD run succeeded → 批次 released + Change 全部 released + 记 ReleaseIDs
     （CD run 内的 deploy/release stage 产出的 Release 反查：SourceRunID=CD run ID）
  4. CD run failed → 批次停在 releasing（可重试 release；代码已合并 main，重试安全幂等）
```

**prod:write 横切**：approve 端点要求 `prod:write`（与 pipeline deploy prod 一致）；release 阶段实际生产部署由 CD 流水线的既有 prod 防护兜底（approve 门禁 + prod:write）。

### 5.4 回滚

批次上线后回滚 = 回滚 CD run 产出的 Release（既有 RollbackRelease，回滚指针链）。批次内 Change 不自动改状态（区分「哪个变更导致回滚」需要业务判断，平台不猜）；预留 `reverted` 状态供手动/后续自动化标记。**本期范围**：回滚走既有 Release 回滚，批次状态不变（released 是历史事实）。

### 5.5 CI run 终态感知

不引入回调机制（YAGNI）。批次详情 GET 时（前端轮询）惰性检查：批次 `testing` 且 RunID 非空 → GetRun 看终态，同步推进批次状态（succeeded→tested / failed→failed）。与 observability 惰性补点同构，无后台 goroutine。`releasing` 同理检查 CD run。

### 5.6 重新集成（conflict/failed 后）

`integrate` 幂等策略：**删除集成分支重建**（从 main 重新创建 + 逐个 merge 当前 ChangeIDs）。简单、无增量合并的状态残留。Gitea 删分支 API（DeleteBranch，新增）。批内变更列表此时已由用户调整（移出冲突/失败变更），重建即反映最新意图。

## 6. Gitea client 扩展

```go
// 新增 3 个 API（internal/devops/gitea/client.go）
CreateBranch(ctx, owner, repo, branch, from string) error          // POST /repos/{o}/{r}/branches
GetBranch(ctx, owner, repo, branch string) (Branch, error)         // GET  /repos/{o}/{r}/branches/{b}（校验已有分支）
DeleteBranch(ctx, owner, repo, branch string) error                // DELETE /repos/{o}/{r}/branches/{b}
```

复用既有 `Merge`（PR 两步合并）与 `CloneURLWithAuth`。

## 7. 存储

- 新包 `internal/devops/change/`：model + Repository 接口 + memory + pg 双实现（与 pipeline 包同构）。
- migration 0026：`changes` 表（tenant_id/app_id/repo_id/title/type/branch/branch_created/base_branch/status/batch_id/created_by/...）+ `integration_batches` 表（change_ids JSONB 有序 + pipeline_id/run_id/release_ids JSONB）。
- 多租户：全方法 ctx tenant 强制过滤（与全仓一致）；跨租户 not found 不泄漏。
- 单实例约束：一个 Change 同时只属一个批次（batch_id 单值）；加入批次时校验 change.status ∈ {open}。

## 8. 关键取舍（YAGNI 砍掉）

- **跨应用批次**：批次绑定单 app 单 repo（merge 发生在同一 repo 内）。跨 app 编排（微服务联合发布）留后续——需要多 repo merge 顺序编排，复杂度陡增。
- **变更级审批**：审批在批次级（approve 一次批一车）。单个变更上线前审批留后续。
- **冲突预检**（merge 前试算冲突）：直接真 merge，冲突即报。预检需要 git 三方合并计算，Gitea API 不直接提供。
- **变更 ↔ PipelineRun 单变更追踪**：变更只在批次维度关联 run，不为每个变更单独跑 CI（单变更验证 = 单分支手动触发既有流水线，天然支持）。
- **PR/MR 实体**：merge 直接走平台，不暴露 PR 概念（无头 Gitea 定位）。
- **外部仓库（external CodeRepo）变更管理**：仅支持 internal（集群 Gitea）仓库——分支创建/合并需要平台对 git 后端的写控制，外部 git 凭证体系不通用。
- **自动批次（定时发车）**：手动批次先行，release train 定时自动成批留后续。

## 9. 前端（console-user）

### 9.1 应用详情新 tab「变更」

`app-tabs/AppChanges.vue`（新）：

- **变更列表**：标题/类型 tag(feat=绿 hotfix=红)/分支(monospace+复制)/状态 tag/所属批次（可点跳批次详情）。
- **创建变更弹窗**：标题 + 类型单选 + 「平台创建分支 / 引用已有分支」单选 + 分支名 + 基分支。创建成功展示 clone 命令（`git fetch && git checkout <branch>`，复制按钮）。
- **放弃**：确认弹窗（非 prod gated——变更本身不触生产）。

### 9.2 批次视图

应用详情「变更」tab 下半区或独立 section「集成批次」：

- **批次列表**：标题/状态/变更数/集成分支/时间；行展开显示变更 chips。
- **创建批次**：标题 + 选择变更（多选 open 状态）+ CI 流水线（默认自动选）。
- **批次详情抽屉**：状态时间线（横向轨道复用流水线视觉语言）+ 变更列表 + 操作按钮（触发集成/批准上线/执行上线/移出变更，按状态显隐）+ 关联 run 链接（跳 `/devops/runs/:runId` 轨道页）。
- 上线操作（approve）走 `useDangerConfirm`（生产语义）。

### 9.3 DevOps 中心

运行记录 tab 的 run 行若关联批次，显示批次标识（branch 以 `integration/` 开头即视觉标记「集成」tag）。

## 10. 权限与审计

- 权限：复用 `pipeline:read/write`（变更域归 DevOps）；approve 上线额外 `prod:write`。
- 审计：change 创建/放弃、batch 创建/放弃/integrate/approve/release 全记（`recordAudit`，action 前缀 `change_`/`batch_`）。
- 多租户：Repository 强制过滤；Gitea 操作经平台账号（与既有 CodeRepo 同模型）。

## 11. 测试策略

- **unit（memory）**：状态机全转移（change/batch 各状态进出）、integrate 编排（fake Gitea：成功/中途冲突）、release 编排（成功/中途冲突）、惰性终态推进（testing→tested/failed）、重新集成重建分支、越权（跨租户 404、缺 prod:write 403）。
- **fake Gitea client**：`change.GiteaBrancher` 接口（CreateBranch/GetBranch/DeleteBranch/Merge）依赖倒置，memory fake 记录调用序列断言 merge 顺序。
- **pg（integration）**：两表 CRUD + JSONB 有序 change_ids 往返 + 租户隔离。
- **e2e（部署后手动）**：创建变更（建分支）→ 本地拉取开发 push → 创建批次选 2 变更 → integrate（集成分支 CI run 泳道隔离）→ approve + release（merge main + CD run）→ 全链路状态核对。

## 12. 实施切分（供 writing-plans 参考）

1. Gitea client 3 API + 测试
2. change 包 model + Repository + memory + 状态机单测
3. pg store + migration 0026 + integration 测试
4. Handler（change + batch CRUD + 编排 integrate/approve/release）+ OpenAPI 登记 + 审计
5. cmd/core 装配（GiteaBrancher 桥接 + pipeline handler 联动：批次触发 run 复用 triggerRunInternal 的等价路径——change 包内自行组 run 或抽公共函数，实施时定）
6. 前端 AppChanges.vue + 批次视图 + DevOps 标识
7. e2e 验证 + 部署
