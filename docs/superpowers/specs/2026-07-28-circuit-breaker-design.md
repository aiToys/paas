# 熔断器切片设计（治理四件套之熔断）

> 日期：2026-07-28
> 范围：服务治理最后一块——熔断器，补齐治理四件套（注册中心 / 配置中心 / API 网关 / 熔断）4/4。

## 背景与定位

治理四件套前三件（注册中心、配置中心、API 网关路由）已落地。本切片补齐**熔断器**：
按服务维度配置熔断规则，在错误率/慢调用率超阈值时熔断目标服务，保护下游、防止级联失败。

属「平台能力（横切）」维度，租户私有，复用服务治理菜单（与路由规则并列）。
逻辑配置不绑定物理环境，**不接 `prod:write`**，复用 `governance:read/write` 权限——与 API 网关路由一致。

## 领域模型

`internal/governance/`（并入既有包，与 Route 同级）：

```go
type CircuitBreaker struct {
    ID, TenantID, Name, ServiceID string
    Strategy    string  // error_rate | slow_call
    Threshold   int     // 触发阈值百分比 (0,100]
    MinRequests int     // 窗口最少样本数（不足不熔断）
    WindowSecs  int     // 统计窗口秒
    Enabled     bool
    UpdatedAt   time.Time

    // 非持久化——由 handler 返回前即时评估填充
    State string        // closed | open | half-open
    Stats WindowStats
}

type WindowStats struct {
    Requests, Failures, SlowCalls, Rate int
}
```

## 即时评估模型（核心设计）

**不接真实流量采集**，用纯函数 `EvaluateBreaker(b, now)` 即时推导 `(Stats, State)`：

- 窗口统计用 `FNV-1a(b.ID + 时间桶)` 确定性生成：`bucket = now.Unix() / WindowSecs`
- `Requests` 范围 `[0, MinRequests*2+20)`——可低于 `MinRequests`（样本不足），也可高于（进入判定）
- `Rate` 范围 `[0,100)`
- 上一窗口通过 `now - WindowSecs` 的桶计算（驱动 half-open 探测态）

三态状态机：

| 条件 | State |
|------|-------|
| 禁用 / `Requests < MinRequests` | `closed` |
| 当前窗口 `Rate >= Threshold`（样本足） | `open` |
| 当前窗口健康 **但** 上一窗口已熔断 | `half-open` |
| 其余 | `closed` |

设计权衡（与项目既有惰性模式一致）：
- **同一 now 下状态稳定**（可重入、可测）
- **跨时间桶演变**（"看起来实时"）
- **无 goroutine**，测试可控（与 metrics/logs/traces/alerts 惰性模式同构）
- `State` 不持久化——真实数据面（Sidecar/SDK 上报滑动窗口计数）接入后，`Stats` 从采集数据取，状态机逻辑保留

## 持久化

`internal/governance/`：
- `Repository` 接口并入 `BreakerStore`：`ListBreakers(serviceID) / GetBreaker / CreateBreaker / UpdateBreaker / DeleteBreaker`
- 单 `Store` 实现全四件套（服务/实例/路由/熔断），方法名带前缀避免重名
- 全方法租户强制过滤，跨租户 not found（不泄漏存在性）；租户内 Name 唯一

## REST API

```
GET    /api/breakers?serviceId=    列表（每条返回前即时评估填充 state + stats）
POST   /api/breakers               创建（governance:write）
PUT    /api/breakers/{id}          更新（governance:write）
DELETE /api/breakers/{id}          删除（governance:write）
```

权限：`governance:read/write`（admin/dev 读写，viewer 只读），**不接 prod:write**（逻辑配置，与物理环境正交）。
handler 在 List/Get/Create/Update 返回前调用 `fillBreakerState` 即时评估填充。

## 前端

`ServiceRegistry.vue` 加「熔断器」section（与 API 网关路由 section 并列）：
- 表格：名称 / 目标服务 / 策略 / 阈值+窗口 / 即时统计（请求·失败或慢调用·百分比）/ 状态（closed 绿 / open 红 / half-open 黄）/ 启停 / 删除
- 创建弹窗：名称 + 选目标服务 + 策略 + 阈值 + 最少样本 + 统计窗口
- 删除走 `confirmDangerous`（与路由删除一致；熔断是逻辑配置，不按环境区分危险等级）

## 不做（YAGNI）

- 真实流量采集（Sidecar/SDK 上报滑动窗口计数）——依赖数据面，留后续
- 持久化 state / half-open 探测的持久化冷却周期——本期即时评估推导，足够展示状态机
- 与路由/发现的联动（熔断时摘除实例或路由降级）——耦合数据面，留后续
- 通知通道、告警联动——可观测切片已覆盖告警能力
