# agentrun × paas pipeline 集成设计（Agent 编排接入）

> 对齐基准：`agent-bianpai/docs/paas-integration-contract.md`（v1）。本设计**替换**契约 §5 的
> Engine 目录 + 独立 6 页面方案（审查结论见 §8 契约修订清单）。集成模式：体验级集成——
> agentrun 代码独立部署，paas 只做编排接入 + 观测入口，不合并仓库。

## 1. 定位与分工

```
需求 ──▶ [agentrun 执行面] ──▶ Gitea 分支 ──▶ [paas pipeline 编排面] ──▶ 上线
          AI写码·沙箱·独立验收      （共享数据面）    build·deploy·release·Change
```

- **agentrun = agent 执行面**（不可替代，不搬进 paas）：沙箱 Pod（NetworkPolicy 全拒入站/出站白名单）、
  runtime 适配（claude stream-json / codex 协议）、独立验收（baseline 对照 + go/npm）、产物 MinIO、
  token 计量、租户预算熔断。裁决标准（CLAUDE.md）：agent 沙箱是 AI 研发专属，非 MaaS/治理/DevOps 通用，不进 Core。
- **paas pipeline = 研发编排面**：从需求到上线的全链状态机，agent 是它的第 9 种 stage 类型。
- **缝合物质**：Gitea 分支（agent 产出即分支，paas build 本来就从分支构建——同一仓库自然接力，非接口拼接）。

## 2. 核心架构决策

> **D10（2026-10-11 追加，用户裁决）双入口共存**：对齐契约 v2 后，委派模式为主入口——paas 变更页
> 「智能研发」按钮（v2 §3.2/§5.1：POST /api/runs 带 workBranch=变更分支，agent 在变更分支工作、
> push 回，paas 批次集成接管）；agent stage 保留为自动化链（webhook 触发的验收/审查等无人发起
> 场景），stage executor 后续支持 workBranch 参数。裁决依据：v2 指出的「一次性意图 vs 可重复流程」
> 语义错位成立（委派模式主入口覆盖人工旅程）；质量证据链（验收/审查/审批门）归 agentrun 执行域；
> 「Change 分支先行」优于本 spec 原路 B 的事后注册。原 D1-D9 中与单一 stage 入口冲突的部分
> （模板主推形态、registerChange 主定位）按此调整；client/代理/权限/审计/超时等机制全部复用。

| # | 决策 | 理由 |
|---|---|---|
| D1 | agent 为新 StageType（`agent`），非独立模块、非 dataservice Engine 条目 | Engine 是数据服务供给目录（KindMetas/凭证/STS/配额），塞进去是领域污染；独立模块则成孤岛无编排衔接 |
| D2 | **编排权唯一归 paas**：agent stage 只调 `POST /api/runs`（单步执行），不用 agentrun 的 `/api/pipelines` 编排与审批 | 大套小 = 双状态机同步地狱（两边审批/重试/终止语义冲突）；多个 agent stage 在 paas 侧经 workspaceFrom 串联，等价复刻其 pipeline 能力 |
| D3 | run 创建显式传参，不用 agentRef 模板继承 | paas 是参数真源，防 agentrun 侧模板隐式变更导致编排链漂移 |
| D4 | **产出=分支归 agentrun，合入归 paas**：pipeline 链不经 agentrun 的 merge API | 合入双轨（见 §4.4）；变更纪律靠 PR 形态 + Gitea 权限保障 |
| D5 | 双通道凭证：编排=静态 token（server-to-server），观测=用户 access JWT（cookie 取值） | 后台 run 可能跑 30min，用户 JWT 15min 过期不能透传；浏览器是 httpOnly cookie 会话，前端无 JWT 可带 |
| D6 | 外部平台地址/凭证走 env：`PAAS_AGENTRUN_URL` + `PAAS_AGENTRUN_TOKEN` | Gitea 先例一比一同构（`PAAS_GITEA_URL`+`PAAS_GITEA_PASSWORD`，空=禁用）；observability 三支柱同模式 |
| D7 | 领域 run 归执行域，paas 只有 StageRun（句柄 + Output） | 永不做大一统 Run 实体（上帝实体反模式）；统一的是协议不是实体 |
| D8 | Gitea webhook 双入口并存：agentrun 自动验收（push→run、PR→审查+评论回写）与 paas pipeline trigger 各自独立 | 不迁——agentrun 贴身自动化（PR 评论回写）是其 operator 能力；Gitea 原生支持多 webhook |
| D9 | 观测面瘦身 2 页只读（运行列表+详情+产物） | 覆盖不经 pipeline 的 run（webhook 自动验收/PR 审查/console 手动）；创建/流水线/模板全省（§5.3 深度运维不重复） |

## 3. 端到端流程（旅程已推演，每步机制有代码级依据）

```
① 建需求    DevOps 触发 run：选 pipeline + branch + 需求文本
            载体 = 触发请求体新增 run 级 stageParams（ParamOverrides 是静态的，需求每跑必变）
② agent(develop) stage：
   - spec 组装：request/runtime 从 stage params；project.git 经 Gitea.ResolveRepo(appID)
     组装（engine.go 已有桥接）；project.ref = run.Branch；name = <runID>-<stageName>
   - POST /api/runs（静态 token）→ 轮询终态（execBuild 的 Create+Poll 同款模式）
   - 成功 → sr.Output{branch, runName, tokens}；失败 → verdict 进 Error
   - Retry 重跑时 workspaceFrom = 上次 runName（agentrun 已设计该字段，迭代修复）
   - Abort → ctx.Done 退出轮询 + DELETE /api/runs/{name} 级联取消
③ agent(review) stage（可选）：同款，stage=review，workspaceFrom 续接
④ approve：RunPaused 等人批 → Resume（现有，零改动）
⑤ build：branchSource=priorAgent → resolvePriorOutput 取 ② 的 branch（branchOverride 现为静态值，扩展 ~10 行）
⑥ deploy(联调泳道) + test(smoke)：现有，零改动
⑦ 合入双轨（模板决定）：
   路 A baseline 直合：mergeFrom=priorAgent 扩展（~10 行）
   路 B Change 火车（正统）：agent stage 成功后 paas 内部注册
        Change{Branch:产出分支, BaseBranch:run.Branch, BranchCreated:false, Status:open}
        → 批次集成 → 发车（不违反单向依赖：由 paas 执行器注册，非 agentrun 反调）
⑧ promote 生产：阶序 + prod:write + approve 门禁（现有，零改动）
⑨ 观测：StageRun.Log 摘要（run 名/phase/verdict/tokens/分支）+ 2 页只读深链
```

## 4. 详细设计

### 4.1 执行域协议（统一标准，agent 是 v2 样板）

现有四件套（隐式约定，物理载体 = engine 桥接接口，BuildRunner 是第一实例）：

```go
// ① 提交 → 领域实体（执行域自持）；② 阻塞轮询到终态；③ 取消 = ctx 传播；④ Output 归一化
type BuildRunner interface {
    CreateBuildRun(ctx, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error)
    PollBuildRun(ctx, buildID string) (devops.BuildRun, error)
}
```

agent 按同形态新增 + 一处增强（**协议 v2：显式 Cancel**——agent run 花 token，「放弃等待」不够，要传播取消）：

```go
type AgentRunner interface {
    CreateRun(ctx, spec AgentTaskSpec) (AgentTaskRef, error)
    PollRun(ctx, ref AgentTaskRef) (AgentTaskStatus, error) // {Phase, Verdict, Branch, Tokens}
    CancelRun(ctx, ref AgentTaskRef) error
}
```

配套纪律：产出进 `StageRun.Output` 规范键（imageId/branch/runName/未来 reportId+passed）；
句柄进 `sr.Input`（前端深链执行域）；凭证 env 三件套（`PAAS_<域>_URL/TOKEN`）。

协议演进路线（均不现在动手）：第 3 个长任务执行域接入时提取显式 `StageExecutor` 接口；
任务量级上来后 Poll 改回调；`resolvePriorOutput` 需要结构化产出时扩展类型。
非长任务型（approve/快速操作）明确不套协议，留在状态机——两分法本身是标准的一部分。

### 4.2 后端改动清单

| 改动 | 位置 | 量级 |
|---|---|---|
| `StageAgent = "agent"` 枚举 + `execAgent`（Submit+Poll+Cancel+Output） | `internal/devops/pipeline/engine.go` | ~200 行（核心新增） |
| `AgentRunner` 接口 + agentrun HTTP adapter（cmd/core 装配，env 注入） | `internal/devops/pipeline/` + `cmd/core/main.go` | 小 |
| build `branchSource: priorAgent`（resolveBranch 扩展） | engine.go execBuild | ~10 行 |
| baseline `mergeFrom: priorAgent`（路 A） | engine.go execBaseline | ~10 行 |
| agent 成功后注册 Change（路 B，参数控制开关） | engine.go execAgent 尾部 | 小 |
| 触发 API run 级 `stageParams`（需求文本载体） | handler.go serveRuns | 小 |
| Retry 带 workspaceFrom（从上次 Output 取 runName） | engine.go Retry | 小 |
| 观测代理 `/api/agentrun/*` → agentrun（Authorization 从 paas_access cookie 注入；ns 服务端强制=agent 剥离用户传入；未配置 503；上游错 502 脱敏；Operation 登记） | `internal/agentrun/proxy.go` 新包 | 小 |
| 权限 `agentrun:read/write`（developer 读写/viewer 读）+ 写方法轻量审计 | identity/model.go + 装配 | 小 |

### 4.3 前端（console-user，2 页只读 + pipeline 表单增强）

- 「Agent 运行」2 页：列表（名称/阶段/模式/优先级/状态/判定/tokens/耗时）+ 详情（KV/结论/产物点开看内容/取消按钮）；数据走 `/api/agentrun/*` 代理（fetchJSON 智能解包兼容裸 JSON）；503 显示未配置引导态。入口归 DevOps 区（研发链路语境）。
- pipeline 模板/编辑器：stage 类型加 `agent`（参数表单：request/runtime/stage/deadlineSeconds/是否注册 Change）。
- 触发表单：支持填需求文本（run 级 stageParams 注入）。
- 可选 UI 甜层：「发起 AI 需求」快捷表单（选 app + 填需求 → 自动套 builtin 模板触发 run）。

### 4.4 builtin 模板（复杂度藏模板里，用户零新心智）

- `tpl-agent-ship`：agent(develop) → agent(review 可选) → approve → build → deploy(联调泳道) → test(smoke) → Change 注册（路 B）
- `tpl-agent-review`：agent(review) → 产出 PR 结论（轻量验证链）

### 4.5 部署配套

- helm values core env：`PAAS_AGENTRUN_URL`（默认空=模块禁用）+ `PAAS_AGENTRUN_TOKEN`。
- 联调：core env 配 `http://agent-console.agent.svc`（Service :80；netpol 只约束 runner Pod 不约束 console，连通无阻）。

## 5. 授权模型（分层，非「只授 paas」）

| 层 | 归属 |
|---|---|
| 用户身份 | paas（agentrun 无本地用户体系；0.13.0 起对 paas access JWT **本地 HMAC 验签**，共享 PAAS_JWT_SECRET，不回调 paas——2026-10-11 对方会话确认，旧「回调验证」是 0.12.x 已删实现） |
| Gitea 实例 | paas（内置无头部署；agentrun 仅 API 消费者，bot 最小权限：建分支/PR/评论） |
| 交付编排/变更/合入 | paas pipeline（唯一大脑） |
| agent 深度运维（模板/预算/AgentPipeline/审批日志） | agentrun console（§5.3 不重复） |
| agentrun 平台自身运维 | K8s 层 |

paas 持 agentrun 静态 token = 调用凭证非管理权；agentrun 持 paas 的仅共享 JWT secret（本地验签用）与 K8s SA。

## 6. 与现有模块关系（重叠检查结论：零重复）

- `internal/ai/agent`（聊天智能体：模型+工具+KB+SSE 对话）≠ agentrun（AI 研发执行器）——同词不同物，不合并。
- `internal/devops/pipeline`（交付流水线）与 agentrun AgentPipeline（AI 阶段编排）是同构两段，集成后编排权归一（D2）。
- agentrun 保留完整独立价值：webhook 自动验收、独立 run、console 深度运维照旧。

## 7. agentrun API 消费边界

| 端点 | 用途 | 通道 |
|---|---|---|
| `POST /api/runs`、`GET /api/runs/{name}`、`DELETE /api/runs/{name}` | 编排（创建/轮询/Abort 级联取消） | 静态 token |
| `DELETE /api/runs/{name}`（观测页「取消」按钮） | 用户手动取消独立 run | 用户 JWT |
| `GET /api/runs`、`GET /api/runs/{name}/artifacts(/key)` | 观测 2 页 | 用户 JWT |
| `/api/pipelines` 全系列、`/api/defs`、`retry`、`merge`、`/api/status`（可选） | **不用**（AgentPipeline/模板/合入独立场景自留） | — |

## 8. 契约修订清单（需同步 agentrun 侧更新契约文档）

1. **§5 整体替换**：Engine 目录条目 + 6 页面 → agent StageType + env 配置 + 2 页观测（本设计 §2 D1/D6/D9）。
2. **§2 补字段**：run 详情响应的「产出分支名」字段必须明确（编排链的 Output.branch 依赖它）。
3. **§3 明确**：`ns` 由 paas 代理/执行器服务端强制（v1=agent），不接受客户端传入（ns=租户边界，防越权）。
4. ~~**§1 与现状对齐**~~（已闭环 2026-10-11）：agentrun 0.13.0 已从回调验证改为**本地 HMAC 验签**（与契约 v2 §1 一致），双方两通道设计确认兼容。**联调前置**：paas 固定 `PAAS_JWT_SECRET`（chart `auth.jwtSecret` 已有配置位，dev 需显式配值）+ 同值同步 agent ns Secret `paas-jwt-public`（当前为演示随机值）；`actor` 归因在编排通道为 `agent:<pipelineRunID>-<stage>`（name 编码）。
5. **agentrun console 访问授权粒度**：回调只验「paas 有效用户」无角色对照——建议补访问策略（对齐 paas 角色或文档化「全员可用」是有意决策）。

## 9. 联调验收（契约 §6 + 增补）

- [ ] 原有：admin 从 paas 进入看到运行数据；demo run→Succeeded→产物可看；审批放行 actor 归因；无效 JWT 401；合入 PR 出现且评论含操作者（注：合入走 paas 链，此条调整为 Change/PR 路径验证）
- [ ] 增补：paas pipeline（agent→approve→build→deploy）全链绿，build 镜像来自 agent 产出分支
- [ ] 增补：Abort pipeline run → agentrun 侧 run 被取消（DELETE 级联生效）
- [ ] 增补：Retry agent stage → 新 run 带 workspaceFrom
- [ ] 增补：Change 注册成功且批次集成可发车
- [ ] 增补：`PAAS_AGENTRUN_URL` 空 → 观测页引导态 + agent stage 明确报错，平台其余功能不受影响

## 10. 非目标（防范围蔓延）

- 计费对接（token→paas billing）：留 v2，接口另行约定（契约 §5.3）。
- AgentDefinition 模板管理、AgentPipeline UI：agentrun console 自留。
- 大一统 Run 实体、需求工单实体（Demand/Issue 状态机）：YAGNI。
- 通用 StageExecutor 接口提取：第 3 个执行域接入时做。
