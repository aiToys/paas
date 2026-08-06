# 整体架构大审查：设计一致性与简化机会

日期：2026-08-05
范围：基于"应用级 Key + 绑定落地"这轮工作的视角，系统审查全平台的**概念一致性、维度归因、闭环完整性、简化机会**。

## 审查视角（来自刚解决的模式）

刚解决的"模型绑定"问题暴露了 4 类典型设计 gap，本审查用同样视角扫全平台：

1. **声明但不落地**：UI/数据模型声明了某种关联，但运行时无效果（原模型绑定即此类）。
2. **维度归因不一致**：以应用为主线，但某类用量/资源归因不到应用。
3. **概念重叠双轨**：同一概念有两个入口/两套实现，用户困惑。
4. **链路断点**：流程写到一半，最后一公里缺失（原数据服务绑定断在 Pod 注入）。

## Top 改进项（按价值×可实施性排序）

### A. 概念重叠双轨（用户困惑，高价值）

#### A1. 三套"配置/密钥"体系并存 → 统一心智模型 🔥最高
现状三个模块都管"键值对/密钥"，用户不知道去哪：
- **appconfig**（应用详情→配置）：应用×环境级，静态，重启注入（env/Secret）。
- **configcenter**（平台能力→配置中心）：命名空间级，动态版本，灰度发布。
- **security**（平台能力→安全）：租户级密钥/证书资产（KMS）。

**问题**：appconfig 的 Secret 和 security 的 Secret 同名概念两入口；configcenter 和 appconfig 都叫"配置"。

**建议**：
- 不合并实现（各有定位），但**统一命名 + UI 互链 + 文档一张图**：
  - appconfig → 改名"**运行配置**"（应用级，随应用走）。
  - configcenter → "**动态配置**"（跨实例，热更新）。
  - security → "**密钥保险柜**"（租户级凭证资产，被引用）。
- appconfig Secret 项支持"引用 security 密钥"（值 = `secret://<security-name>`），消除两套 Secret 明文。

#### A2. DevOps 双视图（应用详情 tabs vs DevOps 中心）
现状：应用详情有构建/镜像/发布 tab，DevOps 中心也有跨应用总览，**完全独立两套 UI**，互不引用（探索报告确认）。

**建议**：上一轮已加"跨应用总览→"链接。进一步：DevOps 中心点构建/发布行 → 跳对应应用详情的 tab（反向链接）。低工作量。

#### A3. 服务治理 Instance 真源双轨
现状：governance 手动注册 mock Instance + dataplane 从 K8s Endpoints 读 Instance。两套 Instance 来源。

**建议**：明确"governance Instance = 手动声明（用户意图），dataplace Instance = 运行时实际（K8s Endpoints）"——UI 分两栏对照（声明 vs 实际），而非各是一个孤岛。

### B. 维度归因（计费一致性，中高价值）

#### B1. billing 其他资源未按应用归因 🔥
现状：刚做了 token 按应用归因（ByApp）。但 workloads/gpu/storage 仍是租户级计数（Counts），不归应用。

**问题**：账单能拆 token，拆不了"应用 A 用了多少 GPU/存储"，与应用主线不一致。

**建议**：工作负载天然归属应用 → GPU/存储用量（从工作负载 spec 派生）归 ByApp。token 是运行时计量，GPU/存储是声明态计量，归因路径不同但都到 ByApp。

#### B2. agent 软标签未在前端暴露
现状：Meter 已记 `user` 字段（OpenAI 软标签），billing 未聚合 user。

**建议**：billing ByApp 内层加 user tag 聚合（看板"应用 A 内哪个 agent 消耗"）。YAGNI 判断：仅当用户有多 agent 编排需求时做。

### C. 闭环断点（功能不完整，中价值）

#### C1. WorkloadReconciler 双向同步缺失
现状：CRD spec → K8s Deployment（正向 OK）；但 K8s 实际 ready → PG/memory store.Ready（反向同步）未做（PG Ready 实时反向同步留后续）。

**问题**：CLI/API 创建工作负载后，store.Ready 不随 K8s 实际状态更新（永远 0 或创建时值）。

**建议**：reconciler 回写 CRD status.ready 后，同步 Update store.Ready（或前端读 CRD status 而非 store）。

#### C2. configcenter 客户端发现是 pull（无 watch）
现状：客户端主动拉 version 比对，无长连接 watch（CLAUDE.md 标注留后续）。

**建议**：YAGNI，pull + 短轮询够用，除非有强实时需求。低优先级。

### D. 命名/路径一致性（简洁性，中价值）

#### D1. /resources/* 在 admin vs user 含义不同
现状：console-admin `/resources/applications`（跨租户总览，super_admin）vs console-user `/resources/db`（租户资源中心）。同前缀两语义。

**建议**：admin 用 `/admin/resources/*`（与 `/api/admin/*` 对齐），user 保持 `/resources/*`。前端 path 改动较大，权衡。

#### D2. 应用 Env 字段是展示值非 EnvID
现状：`Application.Env` 是"生产/预发/开发"展示字符串，非 EnvID。环境是独立实体（EnvID）。概念混淆。

**建议**：应用绑定到环境用 EnvID（多对多已支持），展示值由 environment.Name 派生。改 Application.Env → EnvIDs[]。

### E. 简化机会（减少冗余，低-中价值）

#### E1. 三个前端基座共享组件未抽包
现状：console-user/console-admin 各自有 Icon/EmptyState 等同款组件，未共享。

**建议**：抽 `frontend/shared` workspace（pnpm workspace 内部包）。YAGNI 判断：当前重复可接受，组件稳定后才抽。

#### E2. memory/pg 双实现的样板
现状：每个模块 memory + pg 两套，大量样板。

**建议**：YAGNI，双实现是有意（dev 零依赖 vs 生产持久化），不合并。

## 优先级建议（立即做 vs 后续）

**立即做（本轮，高价值低风险）**：
- **A1（配置三体系统一命名 + 互链）**：解决用户最大困惑，纯 UI/文档 + appconfig 引用 security。
- **C1（workload ready 反向同步）**：闭环断点，影响"看起来假"。
- **A2 反向链接 + A3 对照**：低工作量。

**后续（需求驱动）**：
- B1（GPU/存储按应用归因）：token 已示范，模式可复用，但需真实 GPU/存储计量采集。
- D1/D2（命名重构）：破坏性，等大版本。
- B2（agent tag 看板）：等真实多 agent 场景。

## 结论

平台核心架构（应用为主线 + 三层抽象 + 多租户隔离 + 插件化）是健全的。主要 gap 在**概念呈现层**（配置三体系、双视图）和**闭环最后一公里**（ready 反向同步），不是架构级问题。建议优先 A1 + C1。
