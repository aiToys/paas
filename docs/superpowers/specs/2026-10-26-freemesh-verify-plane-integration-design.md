# freemesh 验证平面集成设计（发布与 AI 研发闭环的机器验收）

> 日期：2026-10-26 ｜ 状态：草案（待评审）
> 关联：`docs/superpowers/specs/2026-10-11-agentrun-pipeline-integration-design.md`（执行域 adapter 先例）、freemesh `examples/paas-bridge`（PoC）

## 1. 背景与动机

freemesh（go-ingress 生态，Zeus 内核家族）的验证平面已在 paas-bridge PoC 中验证可行：

- **零侵入**：只读 paas svc 清单（selector 标签 `paas.aitoys/{service,lane}` → 服务/泳道拓扑）
- **契约验证**：真实响应 JSON Pointer 逐字段断言（PASSED/REAL，命中 mock 即 FAILED——防假绿）
- **灰度路由验收**：染色请求断言 LANE_HIT + 精确落泳道实例
- **三态诚实语义**：PASSED / FAILED / INCONCLUSIVE（不可判定≠失败，如控制面重启 Fence、非 JSON 媒体）

paas 侧闭环缺口（本设计要补的）：

| 闭环 | 现状缺口 |
|---|---|
| 发布闭环 | deploy → approve 之间无机器证据，人工点域名看 |
| AI 研发闭环 | agent-ship 冒烟 = HTTP 探活（2xx 即过），验不了「做的对不对」 |

## 2. 范围

**做**：
- P1：smoke stage 接入 freemesh verify（env 门控，最小闭环）
- P2：契约源升级（paas OpenAPI → freemesh contracts 派生）+ 场景自动派生
- P3：canary 验收 + 发布后自动触发

**不做**（明确边界）：
- 不动 freemesh 流量面（zeus 内核/node proxy 是 freemesh v2 另案）
- 不做 verify 结果的反向写回（paas 只消费结果，不修改 freemesh spec）
- 不引入整链贯穿验证（依赖流量面，当前诚实边界保留）

## 3. 架构

```
pipeline engine（smoke stage）
    │ 类型断言可选能力（WorkloadProber 同款模式）
    ▼
FabricVerifier 接口（pipeline 包定义，依赖倒置）
    ▲
    │ 装配层桥接（cmd/core）
freemeshClient{ base=PAAS_FREEMESH_URL, token=PAAS_FREEMESH_TOKEN }
    │ HTTP
    ▼
freemesh fabric control（freemesh-vnext ns，NodePort）
    POST /v2/runs（scenario_id + Idempotency-Key）→ 202 + run_id
    GET  /v2/runs/{id} → RunMeta{Status, InconclusiveReason, ...}
```

## 4. 契约设计

### 4.1 adapter 接口（internal/devops/pipeline，engine 类型断言）

```go
// FabricVerifier 可选能力：冒烟段经 freemesh 验证平面执行场景断言。
// 未注入（nil / env 未配）时 smoke 保持现状探活，verify 记 skipped——模块降级不阻断。
type FabricVerifier interface {
    // VerifyRun 触发场景运行并等待终态（内部 Create+Poll，超时由实现控制）。
    // idemKey 幂等键（确定性：pipelineRunID+stageIdx），重试安全。
    VerifyRun(ctx context.Context, scenarioID, idemKey string) (FabricRunResult, error)
}

type FabricRunResult struct {
    RunID     string
    Status    string // PASSED / FAILED / INCONCLUSIVE
    Reason    string // INCONCLUSIVE 原因（CONTROL_RESTART 等）/ 失败摘要
    StartedAt time.Time
    Duration  time.Duration
}
```

### 4.2 三态映射（fabric Run → CheckResult）

| fabric Status | CheckResult.Status | 语义 |
|---|---|---|
| PASSED | passed | 场景断言全过（含 LANE_HIT/契约逐字段） |
| FAILED | failed | 断言失败（含「命中 mock 即 FAILED」的假绿拦截） |
| INCONCLUSIVE | skipped + Note 原因 | 不可判定≠失败（控制面重启 Fence/非 JSON 媒体）；如实透传原因，不冒充通过 |
| 接口错误/超时 | failed + Note 网络错误 | fail-closed（验证器不可达时不能静默当通过） |

CheckResult 落 `Type: "freemesh-verify"`，`Target: scenarioID`，`Expected: "scenario PASSED"`，`Actual: Status+Reason`。

**冒烟整体判定**：探活照旧（2xx 必要条件）+ verify（若启用）非 FAILED。INCONCLUSIVE 不阻断流水线（诚实：证据不足不等于失败），但 Note 明示。

### 4.3 场景选择

- P1：stage 参数显式指定——`smoke` stage params 增加 `verifyScenario`（scenario ID 字符串）。未指定则跳过 verify（向后兼容，模板不用改）
- P2：自动派生——deploy 段的 service+lane → scenario 命名约定（`<service>-<lane>-lane` / `<service>-contract`），配合契约生成器（§5）

### 4.4 配置（env 三件套，agentrun 同款）

| env | 语义 |
|---|---|
| `PAAS_FREEMESH_URL` | fabric control 根地址（如 `http://freemesh-vnext.fabric:9090` 或 NodePort）；空=模块禁用 |
| `PAAS_FREEMESH_TOKEN` | control admin token（集群 Secret freemesh-admin 同源）；空=禁用 |
| 超时 | 代码常量（Create 10s + Poll 循环 120s 上限），不入 env（YAGNI） |

## 5. P2：契约源升级（逆向 → 派生）

现状 paas-bridge 契约是 2026-10-25 探测的逆向示例，paas-shop 演进即过期。升级路径：

- paas 已有 **OpenAPI route registry 单一真源**（`internal/apiroute`）+ workload 声明（service/port/domain）
- 新增导出器：workload×service → freemesh contracts YAML（JSON Pointer 断言骨架：status code + 关键字段存在性；响应 schema 校验等级可后置）
- 触发：发布/部署后派生（与拓扑同步 watch 化同批，freemesh 仓实现；paas 侧只保证导出端点稳定）

## 6. 安全

- token 走平台 Secret 体系（`sec-platform-freemesh`，scope=platform，注入 env；参照 sec-platform-airouter 先例——不入库、掩码）
- fabric control 在 fabric ns，跨 ns 调用走 ClusterIP/NodePort，不出集群
- 审计：verify 触发记录进 StageRun.Checks（已有），无需新增审计面

## 7. 验收标准（P1）

1. 未配 env：smoke 行为与现状逐字节一致（回归零影响）
2. 配 env + `verifyScenario` 指向 paas-bridge 四场景：
   - bff-products-real → CheckResult passed
   - 人为改坏契约（或指向不匹配场景）→ failed，流水线 smoke 段 fail
   - INCONCLUSIVE 场景 → skipped + Note，流水线继续
3. 幂等：同 run 重试（Retry stage）不产生重复 fabric run（Idempotency-Key 确定性）
4. e2e：agent-ship 触发一条龙，冒烟段出现 `freemesh-verify` CheckResult

## 8. 分期

| 期 | 内容 | 仓库 |
|---|---|---|
| P1 | FabricVerifier adapter + smoke 集成 + env 三件套 + 测试 | paas |
| P1 配套 | fabric control 稳定性确认（runs API 幂等/超时语义已在 PoC 验证） | freemesh（只读依赖） |
| P2 | 契约派生导出器 + 场景自动派生 + 拓扑 watch 同步 | 两仓各半 |
| P3 | canary 验收接入 + 发布后自动触发（pipeline hook） | paas |
