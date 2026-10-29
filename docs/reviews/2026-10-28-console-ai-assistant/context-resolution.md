# 上下文解析实现记录（spec 2026-10-29）

设计：`docs/superpowers/specs/2026-10-29-console-ai-assistant-context-resolution-design.md`
日期：2026-10-29　|　分支：console-v2　|　迁移：0062

## 一、实现范围（对照 spec §11 七步）

1. **模型兼容**：`DiagnosticContext` 挂 `Session.Context`（JSONB，migration 0062）；旧 `TargetRef` 会话解析后回填 context，行为零变化（测试锁）。
2. **NL 解析**：`ContextResolver.ResolveText`（`internal/assistant/context_resolver.go`）——wl- ID / rule- 告警 ID / 16|32 位 Trace ID 三正则确定性提取 + 受控名称匹配（精确名/分词名，大小写不敏感）；模型提取候选留后续（spec §4 注）。
3. **Alert/Trace resolver**：窄接口 `AlertEventFinder`/`TraceFinder`（service 依赖倒置）；告警 app 级展开 service 候选、dataservice 如实不支持；Trace 走 `real.TracesStore.GetTrace`（TenantEntityLister 白名单，跨租户统一 NotFound fail-closed）+ span service.name 匹配本租户工作负载。
4. **API/迁移/审计**：`POST /api/assistant/sessions/{id}/context/resolve`（candidateId 服务端生成绑定 tenant/user/session，TTL 15min 一次性消费）；审计 `assistant.context` / `assistant.context.resolve` / `assistant.context.forged`（伪造候选 404 + 审计）；OpenAPI 登记。
5. **前端**：删空会话必选下拉；四快捷提示；「已识别上下文」卡（scope-bar 双态）；内嵌候选卡（选择前不调工具不耗预算明示）；最小追问；Observability 告警「诊断」按钮 + Trace 深链 preset.context；历史来源类型标签。
6. **回归测试**：`context_test.go` 15 用例 + PG `TestSessionContextRoundtrip`（抓出跨租户 update 真隔离 bug）。
7. **集群验收**：见下文「四、集群验证」。

## 二、关键设计决策（与 spec 的偏差/细化）

- **409 契约平级字段**：`{error:"target_resolution_required", resolutionStatus, question, candidates?, anchor?}`——保持平台 `{error:msg}` 错误契约的同时携带结构化负载；不建 run、不预留 token、budget 调用计数为零（测试断言）。
- **Session.Target 双语义收敛**：resolved 会话 `Session.Target` 同步写 AuthorizedTarget，executor/worker/budget 执行链路零特判；`Target.WorkloadID==""` 即 draft。
- **候选防伪造**：Service 内存 `map[candidateID]candidateEntry`（tenant/owner/session 三绑定 + 过期时间），resolve 时校验绑定 + scoped 重新解析（不信任缓存的 AuthorizedTarget）；一次性消费防重放；过期与伪造区分（过期 404 不误报审计）。
- **解析窗指针化**：`DiagnosticContext.WindowStart/End` 用 `*time.Time`（nil=未定窗）——`time.Time` 是 struct，`omitempty` 拦不住零值序列化（本地冒烟抓出 `"0001-01-01T00:00:00Z"` 外泄）；告警/Trace 解析窗对齐事件时间（±15min pad），`lookbackMinutes` 显式指定优先。
- **CreateRun 顺序**：入参校验提前（draft 会话先有合法 question）→ draft 解析 → Authorize（Target 空会 Authorize 失败，故解析在前）。

## 三、顺带治本：apiroute 跨包撞名（既有 bug）

重新生成 `assistant.gen.ts` 时发现 Evidence schema 变成 `{detail,source,weight}`——`diagnose.Evidence` 与 `scoped.Evidence` 跨包同名，registry 按裸类型名登记，后注册者**静默覆盖**前者（assistant 的 Evidence schema 自上线即错）。

修法：`Registry.schemaTypes` 记录类型全路径 `t.String()`，同名不同包用「包.类型」限定名分离登记（`scoped.Evidence`），先注册者保短名；`internal/apiroute/nameclash_test.go` 用真实类型锁行为。前端 `Evidence` 类型改引 `Schemas['scoped.Evidence']`。

## 四、验证记录

- `make test`（内存）全绿：assistant 包 15 个新用例（唯一名称自动确认 / wl- ID / 多候选 409 预算零调用 / 候选确认建 run / 伪造 404+审计 / 告警 workload 级窗口对齐 FiredAt / 告警 app 级 2 候选 / 告警跨租户 fail-closed / Trace 归属不可证拒绝 / Trace 唯一服务 resolved / Trace 多服务候选 / 无线索最小追问零 run / 旧 target 兼容 / 第二 Agent 复用+inputKinds / 候选 TTL 过期）。
- `make test-pg`：`TestSessionContextRoundtrip` 首跑抓出 UpdateSession 跨租户写漏洞（WHERE 用 sess.TenantID 显式参数，跨租户 ctx 也能改他租户行）——修复为 tenant_id/owner_user_id 一律从 ctx 取（不信任 sess 字段），RowsAffected==0→ErrNotFound。
- `make lint` 0 issues；vue-tsc 全绿 + pnpm build 过。
- 本地冒烟（内存模式）：draft 会话创建（context=need_clarification 且不下发 target）/ 无线索 409 `{error,question,resolutionStatus}` / 伪造候选 404 / 非法 context kind 400 / alert 锚点无 PG 告警数据 400 unavailable。

### 集群验证（2026-10-29，deploy-k8s.sh 后）

- draft 会话创建 201（context=need_clarification 不下发 target；PG migration 0062 写入即证列存在）。
- 名称唯一匹配（paas-shop-order-prod）→ **202 自动 resolved**，target/context 落库（source=text、anchors、±30min 窗）。
- 多候选（「product」2 匹配 测试/生产-北京）→ 409 need_choice + 候选带环境名；候选确认 resolve → 200 resolved；重复 resolve → 409。
- 告警锚点（集群真实 app 级告警 rule-e97ad6f11296）→ 15 候选（app 全量 service）→ 确认后 resolved，anchors=[alert, workload] 可追溯，**窗口对齐 FiredAt-15m → OccurredAt+15m**，source=alert。
- 伪造候选 → 404 + 审计 `assistant.context.forged`；伪造 wl- → 409 need_clarification（不泄漏存在性）。
- 跨租户（globex 读 acme 会话）→ 404。
- 旧会话（10-28 上线期 2 个）+ 14 条消息流读取正常（旧 target 语义零变化）。
- 审计三连落库：`assistant.context` / `assistant.context.resolve` / `assistant.context.forged`。

### 集群首验抓出：service 层漏 WithOwner（PG 路径静默 404）

集群首验名称解析 404、无线索 409——分叉锁定 resolved 路径。根因：**service 层调 `repo.UpdateSession(ctx, sess)` 用的原 ctx 无 owner**（OwnerFrom 只认 WithOwner 注入），PG 层 `WHERE owner_user_id=OwnerFrom(ctx)` 得空串 → 0 行 → ErrNotFound → 404。内存 repo 用 sess 字段不依赖 ctx，单测全绿没抓到。

修复（两处）：resolveQuestionContext 与 ResolveContext 的 UpdateSession 调用补 `WithOwner(ctx, s.ownerFrom(ctx))`；**memRepo.UpdateSession 同步改为 ctx owner 精确匹配**（与 PG 同语义，此后这类漏注入内存测试即红）。教训与「pipeline runCols 缺列」「PG 仓储集成测试必须补」同族：**内存/PG 双实现语义必须对齐，否则单测绿灯是假象**。

### 真实模型验收（2026-10-29，用户确认后跑，目的地 airouter）

`tmp/context-resolution/real-acceptance.sh`，5/5：

1. **告警深链 investigate**（15 候选确认后的会话）：completed + sufficient + 8 证据 + provider usage 10909 tokens；真实诊断出 paas-shop-product 生产 2 Pod CrashLoopBackOff（各重启 47 次）+ 根因假设「DATABASE_URL 未注入」。
2. **同会话 explain**：completed，reply 明确标注「根据 …历史观测」（不重新取证），**窗口复用会话解析窗（告警 FiredAt 对齐窗）而非 now**；quality 空为 explain 语义（与上线行为一致）。
3. **结果断言**：quality=sufficient、evidence≥5、usageSource=provider 落库。
4. **消息流两轮恢复**：4 条消息（2 问 2 答），候选确认走 resolve 端点不产生对话消息（轮次语义正确）。
5. 副产物：UID 快照替换检测在真实过期会话正确拦截（「目标或实例已被重新创建，请重新选择」）。

**顺带发现的真实故障**（诊断能力展示，非本任务缺陷）：paas-shop-product 生产 Pod CrashLoopBackOff、疑似 DATABASE_URL 绑定注入缺失——与 2026-10-21「appconfig 变更不触发已建 CR 的 env 重渲染」前科同族，建议按当时 runbook（PUT replicas 2→1 制造 spec diff 强制重渲染）排查。

## 六、附：验收当日集群事故与幽灵自愈立项（2026-10-29）

真实模型验收的诊断结果顺藤摸瓜发现**集群级事故**：**9 个数据服务全部成幽灵**（平台 DB 记录 running、K8s CR/STS/Svc 全没了），依赖服务（product/recommend/chatbot/order/mcp 等）因连接串指向不存在的 FQDN 连锁 CrashLoop（部分 Pod 还叠加「空 env RS」——reconciler 在事故窗口渲染出 env 全空的 Deployment 模板）。

**止损**（当日完成）：逐个 GET+PUT 触发重 Apply，9 个 STS/Svc 全部重建；业务 Pod 自愈（CrashLoop 10→0，16 个 Running）；残留空 env RS 手工缩删。**根因未定论**：删除者唯一代码内嫌疑是 OrphanGC，但其删除前提（ListAll 返回空）与 PG fail-fast 矛盾；CR 无 ownerRef 排除 K8s GC；旧 pod 日志已丢无法取证。deliberately 不猜根因。

**治本**（当日立项并上线，`cmd/core/dataservice_heal.go` + `DataServiceK8sApplier.EnsureCR`）：还掉 CLAUDE.md 遗留债务「dataservice 应补周期对账 DB 有行无 STS→重 Apply」——5min 周期跨租户对账，CR 存在跳过（防周期 Apply 引发 STS 滚动，与 workload EnsureIfMissing 同语义）、缺失重投影；与 OrphanGC 判定同源不互搏。单测 `TestDataServiceK8sApplierEnsureCR` 锁三态（缺失重建/存在不覆盖/external 跳过）；已随第三次部署上线。**集群决定性验证已实证**（网络恢复后补验）：删除自建测试 CR（ds-1793267570128915302-1）于 09:54:26 → heal 日志 `[ds-heal] 平台记录存在但 CR 缺失，已重投影` 于 **09:56:02（96 秒自动重建）**，测试实例验证后平台侧删除清理。终验 `reconcile.sh`：F/G/H（幽灵 workload/孤儿 CR/孤儿 dataservice）全空，唯一不通过项 D1 为 paas-shop 旧 tag 历史遗留（非本次事故引入）。

## 五、已知边界（留后续）

- 模型提取候选（spec §4 第四优先级「模型提取」）：当前仅确定性正则 + 名称匹配；无匹配时最小追问，不猜。
- 候选缓存为 Service 内存（单副本语义）；多副本需 Redis（与会话 jti 黑名单同款债务）。
- `log` kind 声明于 ContextInput 但 resolver 尚无独立 log 锚点语义（自然语言日志检索仍在 resolved 目标内做）。
- app 级告警展开候选在多服务应用（paas-shop）达 15 个——候选卡可滚动可用，但「最小化追问」体验打折；后续可按告警 metric/label 预筛或环境分组。
