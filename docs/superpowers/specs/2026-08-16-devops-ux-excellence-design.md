# DevOps UX 业界优秀标准改造设计（收件箱 + 值班台 + 通知 + 对账）

> 日期：2026-08-16
> 状态：已确认（用户「开始执行」）
> 前置：变更管理（Change/IntegrationBatch）已落地（2026-08-15）

## 背景与问题

用户反馈：DevOps 前端交互体验差，各模块不统一不完整——DevOps 中心看不到变更/批次；构建/发布/镜像无详情页；单据间无链路串联；用户需要在多个列表间反复横跳。

诊断根因：**单据（变更/批次/构建/镜像/发布/运行）没有统一的「实体 + 列表 + 详情 + 串联」模型**。

## 设计总纲

**优秀的本质不是功能多，而是让用户「只盯一个地方，且只在需要时被打扰」**：

1. **变更收件箱**（对标 GitLab MR 制）—— 变更详情页 = 全生命周期一站式工作台
2. **主动触达**（L1 站内通知）—— 从「用户轮询」到「系统找人」
3. **值班台 + 档案室**（DevOps 中心）—— 排障视角的聚合首页
4. **持续对账**（克制版 Argo）—— Release 详情展示当前运行态

## 四阶段

### A 补底：单据一等公民（纯前端）

- DevOps 中心补「变更」「批次」两个 tab（跨应用列表，复用 `/api/applications/{id}/changes` 需新增跨应用端点或前端按应用聚合——**决策：后端加 `GET /api/changes?appId=&status=` 与 `GET /api/batches?appId=&status=` 跨应用列表端点**，与 buildruns/releases 同款）
- 六单据独立详情路由：
  - `/devops/runs/:runId`（已有 PipelineRunPage）
  - `/devops/changes/:id`（新建，复用/升级 AppChanges 抽屉内容）
  - `/devops/batches/:id`（新建，el-steps 状态机 + 关联 run/变更/发布）
  - `/devops/builds/:id`（新建，构建日志全量 + 关联 run/镜像）
  - `/devops/releases/:id`（新建，发布信息 + 回滚 + 关联 workload 运行态）
  - 镜像不设独立详情（registry tags 展开行已够，YAGNI）
- 链路串联：详情页前驱/后继链接（change↔batch↔run↔build↔release↔workload）

### B 收件箱：变更一站式工作台（前端为主）

变更详情页聚合五段：
1. 我的代码：分支 + clone 命令 + 最近 commits
2. 集成批次：所属批次 + 状态 + 关联 CI Run 入口
3. 测试验证：批次测试状态
4. 发布状态：审批等待/已发布 + CD Run + Release 链接
5. 时间线：创建→入批→集成→测试→审批→发布全事件流

变更列表加「卡在哪一步」状态列 + 内联下一步操作。

### C 通知：站内通知中心（前后端）

- 后端 `GET /api/notifications` 聚合端点：扫批次（conflict/testing/releasing）+ runs（failed/paused）+ 变更待手测，拼装返回 `{items: [{id,type,severity,title,appId,targetType,targetId,at}]}`
- 前端顶栏铃铛 + 未读红点（localStorage 记已读）+ 下拉通知列表，点击跳对应详情页
- 无新实体表（实时拼装，YAGNI——不做持久化通知）

### D 对账：Release 运行态卡片（纯前端）

Release 详情页底部：该 Release 部署的 Workload 现状（副本就绪比/实际镜像/状态），数据来自 workloads API。

## 明确不做（YAGNI）

- DAG/画布式流水线可视化
- 完整评审流（评审人/评论/diff）
- 自定义流水线 DSL
- 通知持久化/订阅规则/Webhook 出站（L3，等真实需求）
- 漂移检测告警

## 横切约束

- 后端新端点全部 OpenAPI 登记 + `{data:T}` 契约 + camelCase json 标签（回归测试断言 JSON key）
- 多租户隔离：跨应用列表端点强制 ctx tenant 过滤（与 buildruns 同款）
- 权限：pipeline:read/write 域（变更/批次同源）
- 前端：详情组件唯一、两处复用（DRY）；fetchJSON 消费；轮询 10s + onUnmounted 清理
