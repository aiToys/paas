# 配额计费切片设计

> 状态：已定稿 / 蓝图优先级 #7（多租户商业化根基）
> 日期：2026-07-27
> 范围：本期只做**配额 + 用量 + 账单**的查询展示 + 账单生成/支付闭环（进程内 mock）；配额强制拦截（资源创建时阻断超限）留后续横切切片。

## 1. 定位与边界

配额计费是**多租户商业化根基**，属「设置」维度（租户级管理面），租户私有。与既有切片的关系：

- 复用既有计量锚点：API Key 三元组 `(租户, 用户, 角色)`，租户上下文走 `pkg/tenant` ctx。
- **不接 `prod:write`**：配额计费独立于物理环境（与可观测/安全/配置中心一致），不按环境隔离。
- 权限 `billing:read / billing:write`（admin/dev 写、viewer 只读，与 security/governance 一致），并入 `identity.BuiltinRoles`。

## 2. 领域模型

三个实体（单包 `internal/billing`）：

```
ResourceQuota  { TenantID, Limits map[string]int(-1=无限), UpdatedAt }   // 每租户一份
ResourceUsage  { TenantID, Counts map[string]int,        UpdatedAt }   // 每租户当前用量
BillingRecord  { ID, TenantID, Period(YYYY-MM), Items[]BillItem, Total, Status(unpaid|paid), CreatedAt, PaidAt }
BillItem       { Resource, Quantity, UnitPrice, Amount }
```

计费资源维度（常量）：

| 常量 | 含义 | 单价（元/单位，mock） |
|------|------|------|
| `applications` | 应用数 | 10 |
| `workloads` | 工作负载数 | 5 |
| `models` | 模型部署数 | 20 |
| `gpu` | GPU 卡·小时 | 100 |
| `tokens` | token（千次） | 0.001 |
| `storage_gb` | 存储 GB | 0.5 |

单价表为平台级 mock 常量 `PriceTable`（导出，前端可读取对齐），真实计费引擎/计费周期/账单导出留后续。

## 3. 核心流程

- **配额查看/设置**：`GetQuota`（不存在返回默认配额）/ `SetQuota`（admin）。
- **用量查看**：`GetUsage` 返回当前各资源计数；handler 层组装 `UsageView`（含 limit/count/over 超限标记）。
- **账单生成**：`GenerateBill(period)` 取当前 usage × 单价逐项计算，求和得 total，生成 `unpaid` 记录。同 period 已有 `unpaid` 则覆盖更新（避免重复账单堆积）。
- **账单支付**：`PayBill(id)` 将 `unpaid -> paid`，填 `PaidAt`；已支付拒绝重复支付。

本期**不做强制配额拦截**（资源创建时不阻断超限），仅在用量视图标红超限告警。横切拦截需注入到各资源 handler（application/workload/...），归后续切片。

## 4. REST API

```
GET  /api/billing/quota                         读取当前配额
PUT  /api/billing/quota                         更新配额（billing:write）
GET  /api/billing/usage                         用量 + 配额 + 超限标记（UsageView）
GET  /api/billing/records                       账单列表（倒序）
POST /api/billing/records/generate?period=      生成本期账单（billing:write）
POST /api/billing/records/{id}/pay              支付账单（billing:write）
```

权限：读 `billing:read`，写 `billing:write`。全方法租户强制过滤，跨租户 not found（不泄漏）。

## 5. 前端

`console-user` 「设置 → 配额与账单」`/settings/billing`（`Billing.vue`）：

- 配额用量卡：6 资源 ×（当前 / 上限 + 进度条，超限红色告警）。
- 「生成本期账单」按钮（默认本月 YYYY-MM）。
- 账单列表：period + total + status tag + 展开明细 items（资源/数量/单价/金额）+ 支付按钮（仅 unpaid）。

侧栏「设置」下新增「配额与账单」入口（icon: usage 复用或 zap）。

## 6. 横切继承

本切片是平台能力横切的延续（治理/配置中心/可观测/安全 之后的第 5 个横切片）：

- 租户隔离：Repository 全方法从 ctx 取租户强制过滤（与既有切片一致）。
- 不接 `prod:write`（与可观测/安全一致，独立于物理环境）。
- 支付是状态变更而非删除，不走 `useDangerConfirm`；配额调整影响成本，前端配额编辑可加二次确认（轻量，不必输入名称）。

## 7. 不做（YAGNI）

- 配额强制拦截（资源创建阻断）→ 后续横切切片。
- 真实计量采集（从 workload/应用/token 实际派生用量）→ 本期 seed mock 用量。
- 计费引擎（阶梯/套餐/优惠券/税）/对接支付网关 / 账单导出（PDF/发票）→ 后续。
- 用量按周期归档/趋势 → 后续（可观测切片已有时序能力，账单本身即周期快照）。
