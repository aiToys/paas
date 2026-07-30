# 环境 IA 重设计（主辅结合）

> 切片目标：厘清环境在信息架构里的两个角色，消除三入口混乱。
> 诊断见对话：顶栏 scope（全局上下文）与环境菜单（资源实体）边界不清 + Workloads env-bar 重复 + 环境菜单跳工作负载列表（无环境详情页）。
> 方向（已确认）：**主辅结合** --顶栏 scope 为唯一环境切换入口（操作面），环境菜单重定位为管理面（CRUD + 环境详情页）；应用列表不按 scope 过滤但显示部署徽标。
> 约束：**纯前端**，后端不动（前端组合现有 API）。

## 问题诊断（根因）

环境扮演两个相互冲突的角色，边界没厘清：
- (A) 全局工作上下文（顶栏 scope，K8s context 模式）
- (B) 可管理资源实体（环境菜单，多区多云 CRUD）

三个症状同源：
1. Workloads env-bar 与顶栏 scope 重复（操作同一 envStore，视觉像两套）
2. 环境菜单点环境跳 `/workloads/services` --环境详情页缺失，一等公民没有自己的详情
3. scope 语义不统一：工作负载按 scope 过滤，应用列表/应用详情部署 tab 不过滤（未区分运行态 vs 逻辑态）

## 方案：主辅结合

厘清边界，各司其职：

**主 --顶栏 scope（操作面）**：唯一环境切换入口，统管**运行态**
- 工作负载/发布/应用详情部署操作：按 scope 过滤/默认
- 应用列表/模型目录（逻辑态）：**不**过滤，应用卡片显示「scope 环境部署徽标」
- 删除 Workloads 顶部 env-bar（去重）

**辅 --环境菜单（管理面）**：环境实体 CRUD + 跨环境总览
- 环境列表：每环境显示统计（部署应用数/工作负载数/健康度），不跳走
- 点环境 -> 新建环境详情页 `/environments/:id`
- 创建环境入口（后端 POST `/api/environments` 已就绪）

**一句话区分**：顶栏 scope =「我在哪个环境干活」；环境菜单 =「我管理环境本身」。

## 改动清单（纯前端）

| 文件 | 改动 |
|---|---|
| `Workloads.vue` | 删除顶部 env-bar（顶栏统管）；保留按 scope 过滤 |
| `Environments.vue` | 改造为管理面：卡片显示统计 + 「创建环境」入口；点击进环境详情（不跳工作负载） |
| `EnvironmentDetail.vue`（新） | 环境详情：信息卡 + 工作负载总览（按类型）+ 应用部署矩阵 |
| `Applications.vue` | 应用卡片加 scope 部署徽标（前端聚合应用 + scope 工作负载） |
| `router.ts` | 加 `/environments/:id` 路由 |
| `stores/env.ts` | 无改动（currentEnv 语义不变） |

## 关键交互

**应用列表部署徽标**（`Applications.vue`）：
- 加载应用列表时并行加载工作负载（scope 选具体环境按 envId 过滤；scope=全部加载全量）
- 应用卡片徽标：
  - scope 具体环境：「✓ 测试 · 2 副本」或「未部署」（灰）
  - scope 全部：「部署在 N 个环境」

**环境详情页**（`EnvironmentDetail.vue`）：
- 信息卡：名称/类型/cluster（GET `/api/environments` 取一个）
- 工作负载总览：`GET /api/workloads?envId=xxx`，按 service/job/cronjob 分组计数 + 健康度
- 应用部署矩阵：从工作负载反推 appID + `GET /api/applications` 补应用名/图标 -> 表格（应用 / 工作负载数 / 副本就绪 / 状态）
- 「在此环境工作」按钮：点击 = switchEnv(该环境) + 跳工作负载页（把"管理面 -> 操作面"的桥搭上，但不强制）

**环境菜单统计卡**（`Environments.vue`）：
- 每环境卡显示：工作负载数（`GET /api/workloads?envId=` 聚合）+ 健康度
- 点卡进环境详情；独立「创建环境」按钮

## 不做（YAGNI/后续）

- 环境编辑（改名/cluster）--后端无 PUT，后续需要再加
- 环境删除确认走 useDangerConfirm（生产输入名称）--已有后端 DELETE，前端补确认可后续
- 环境级发布历史聚合--需要后端 releases 支持 envId 过滤，归后续
- 应用列表按 scope 过滤--已确认不过滤

## 验收

- Workloads 页无顶部 env-bar；切顶栏 scope 工作负载列表随之变化
- 环境菜单：卡片显示统计；点卡进环境详情页（不再跳工作负载）
- 环境详情页：工作负载总览 + 应用部署矩阵正确
- 应用列表：scope 选测试，应用卡片显示测试部署徽标；scope 全部显示部署环境数
- 顶栏 scope 唯一性：除顶栏外无其他环境切换控件
- 前端三套 build 通过；console-user dev 端到端 Playwright 验证

## 架构约束

- 纯前端 IA 调整，后端 API 不动（mock 期前端组合）
- 保留顶栏 scope + 生产 gated + 视觉强隔离（生产安全横切载体，不动）
- 保留环境一等公民语义（多区多云/泳道需要）
- scope 只管运行态（工作负载/发布），逻辑态（应用/模型目录）不过滤
