# 设计评审与架构优化（2026-08-01）

> 系统性对照一流控制台（Vercel / Linear / Railway / KubeSphere）评估 paas 当前设计与架构，记录已落地改进与后续路线。回应「设计对比 / 架构最优 / 优秀开源标准」要求。

## 1. 设计对比（横向）

| 维度 | 一流控制台惯例 | paas 现状 | 评估 |
|------|--------------|-----------|------|
| 首屏性能 | vendor 分包 + 长效缓存 | console-user `index.js 1055KB→21KB`（本轮分包），element-plus/vue 独立 chunk | ✅ 已对齐 |
| 官网专业度 | logo + 导航 + hero signature + 多 section | landing 重做（6 组件 + 三层架构 signature） | ✅ 已对齐 |
| 空状态/加载 | 一致骨架屏 + 引导文案 | 15/16 视图覆盖 | 🟡 1 视图待补（DRY 抽公共组件，见 §3） |
| 错误反馈 | 操作级 toast + 重试 | 写操作全覆盖；429 全局引导配额页 | ✅ |
| 数据契约 | 单一真源 + 类型生成 | OpenAPI route registry + openapi-typescript | ✅ 已对齐（业界少见的"路由即契约"） |
| 暗色/亮色切换 | 多主题 | 仅深色（控制台 dark css-vars 已铺） | 🟡 后续（用户偏好） |
| 命令面板（Cmd+K） | Linear/Vercel 标配 | 无 | 🟡 后续（seed 数据少，YAGNI 边际） |
| 移动端响应 | 自适应 | landing 全响应；控制台桌面优先 | 🟡 控制台后续 |

**结论**：核心交互层（契约/性能/错误/首屏）已对齐一流；主题切换/命令面板属增量，非开源标准硬门槛。

## 2. 架构评估（纵向）

三层 + 插件是设计阶段定稿（见 `2026-07-26-maas-platform-foundation-design.md`），核心不变量稳固：
- **Core 最小不可分**：业务领域逻辑不进 Core（判据"MaaS/治理/DevOps 都会用吗"）—— 本轮未发现违反
- **子系统是插件**：MaaS 已插件化，治理/DevOps 待按插件接入 —— 设计预留
- **控制面/数据面解耦**：CRD 期望状态 + Reconciler，manager 挂了数据面继续 —— 已验证

### 本轮架构 DRY 改进

**问题**：15 个 handler 包各自重复定义 `writeErr`，5 个重复 `writeData`（实现完全一致）—— 经典 DRY 违反。

**修复**：新建 `internal/httputil` 公共包（`WriteError`/`WriteData`/`WriteDataCreated`/`WriteJSON`），全量替换：
- 消除 15 处 `writeErr` + 5 处 `writeData` 重复定义
- 统一显式设 Content-Type（原 `writeErr` 依赖 handler 前置 SetHeader，漏设时返回 text/plain）
- 行为零变化（机械重构），`go build ./...` + `go test ./...` 全过

**收益**：响应契约集中维护，未来调整（如加 trace id / 统一错误码）改一处生效。

### 其他架构确认（无需改）
- Repository 切换点（memory/PG）：双路径清晰，`PAAS_DB_URL` 二选一
- 横切关注点统一治理：`prod:write` / `useDangerConfirm` / 配额 `CheckAndInc` 各切片继承
- 控制面/数据面装饰器：`ApplyRepo` 透明投影 CRD，devops Release 编排对存储后端透明

## 3. 后续路线（按 ROI 排序，非本期阻塞）

1. **前端 EmptyState/LoadingSkeleton 公共组件**：6 视图重复 loading/empty 结构，抽 `components/common/`（DRY，低风险）
2. **element-plus 按需引入**：首屏再省 ~250KB gzip；需处理服务式组件（ElMessage）样式，回归测试成本中
3. **控制台亮/暗主题切换**：dark css-vars 已铺，加切换开关即可
4. **PG RLS force 注入**：当前 RLS 为安全网模式（session 未设放行），连接级 `SET app.tenant_id` 强制隔离归后续
5. **ClickHouse 时序计量**：billing/observability 当前 PG + 内存，规模化时迁

## 4. 「优秀开源标准」对齐度

| 硬门槛 | 状态 |
|--------|------|
| LICENSE / CONTRIBUTING / SECURITY / CODE_OF_CONDUCT | ✅ |
| CI（test/lint/build + PG 集成 + coverage + 多平台镜像发布） | ✅ |
| README（架构/技术栈/快速开始/部署） | ✅ |
| CHANGELOG（Keep a Changelog 格式，本轮变更同步） | ✅ |
| 代码规范（gofmt 全绿 / go vet / TS 严格） | ✅ |
| 单一契约（OpenAPI 单一真源） | ✅ |
| 双模交付（Helm 在线 + airsync 离线） | ✅ |
| 端到端验证（K8s 集群真实落地） | ✅ |

**结论**：开源硬门槛已齐；增量项（主题/命令面板/按需引入）属体验打磨，不阻塞「优秀开源标准」达成。
