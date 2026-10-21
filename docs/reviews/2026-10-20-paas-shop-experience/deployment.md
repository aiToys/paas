# PaasShop 集群 Git → PaaS 构建发布记录

日期：2026-10-20。用户确认仅发布测试环境；随后明确授权恢复五个现有测试数据服务。

## 代码与构建来源

- 集群 Gitea：`paas-bot/paas-shop`（本次实时核对结果，与旧 runbook 中仓库名不同）。
- 分支：`feat/paas-shop-lane-experience-20261020`。
- 提交：`a3f80e632c03d0e74570063fd4ac67699e723076`。
- 从集群仓库 main 基线创建隔离副本，仅同步本次 PaasShop 的 13 个文件；保留远端已有 setup 等差异。未推送 main、未合并分支、未推送 GitHub、未提交主工作区的其他改动。
- 新提交副本使用实际 vendor 依赖通过 BFF/发现 race 回归；PaaS 从已核实的分支和完整提交 SHA 构建，未使用本机 Docker 代替平台构建。

## 构建与发布

| 服务 | BuildRun | Image | Release |
|---|---|---|---|
| bff | `build-e017ec18f3ba` | `img-98601a3ea22f` | `rel-bf7dd19c6683` |
| frontend | `build-a6ed50d0f903` | `img-cd7f78594f8b` | `rel-66a93ad1ee4e` |

两次 BuildRun 均 success；两次 Release 均 succeeded，复用原工作负载，目标均为 `env-acme-test / default`。

- bff 运行摘要：`sha256:df2412bbb8574844ade6e9439fd6d48554797c5f2e3ba936890951679a707f27`；Pod Ready，imageID 与产物摘要一致。
- frontend 运行摘要：`sha256:16b70767f19302b6efc661ccbc56dcede3f8a520f06b12d2f887aa771480899e`；Pod Ready，imageID 与产物摘要一致。

## 依赖恢复与配置

发布前平台记录仍在，但测试运行资源缺失。自动审批首次拒绝数据服务重投影后，已向用户说明风险并取得明确授权，再通过 PaaS 操作：

- `shop-search`：保持原 ID/规格/凭证，复用 `data-ds-1788316665364162108-5-0`；恢复后 PVC UID 与恢复前一致。
- `shop-vector`：保持原 ID/规格/凭证，复用 `data-ds-1788316665288221176-4-0`；恢复后 PVC UID 与恢复前一致。
- `shop-mq`：保持原 ID/规格/凭证，复用 `data-ds-1788316665134441078-3-0`；恢复后 PVC UID 与恢复前一致。
- `shop-cache`：保持原 ID/规格/凭证，复用 `data-ds-1788316664939400959-2-0`；恢复后 PVC UID 与恢复前一致。
- `shop-db`：保持原 ID/规格/凭证，复用 `data-ds-1788316664687967587-1-0`；恢复后 PVC UID 与恢复前一致。

五个数据服务均已运行；现有 product/recommend/chatbot/order/mcp 服务通过 PaaS 恢复原副本数。BFF/frontend 经 Release 更新。

- 仅测试桶新增 `PAAS_SHOP_APP_ID`、`PAAS_CONSOLE_URL=http://paas.k8s.dd/next/`、`PAAS_ENV_TYPE=test`；不重跑全量 setup，不改已有凭证。
- 生产环境期望镜像与发布前记录逐项一致。

## 真实验收

- 商品 200，返回已有商品；推荐 200，返回 3 个推荐，证明恢复后的数据库/缓存业务链路可访问。
- 平台泳道目录 200，实际环境与产品 ID 正确；当前环境没有 active 泳道，页面诚实显示“暂无活跃泳道”。
- 人工携带未覆盖泳道名的只读请求成功回到 default，响应分别记录 requested/resolved/reason/instance/TraceID。没有创建测试泳道或修改业务订单。
- 浏览器访问真实 frontend→BFF→平台/下游，验证新选择器、实例、Trace 链接、六条能力路径；截图见 `deployed-test.png`。
- 七个测试 Deployment 均 1/1；首次恢复时商品/订单因依赖启动时序短暂重启，随后就绪并通过接口验证。

## 留存边界

- 运行资源原先消失的根因尚未确定，本轮恢复测试依赖，不宣称修复全平台同步/回收问题。
- 当前无活跃泳道，因此真实特性副本命中和无就绪实例的现场故障注入未做；此前本地模拟依赖交互与对应回归测试已覆盖。
- 既有 CronJob 仍缺失，本轮未恢复 sweeper/statsworker，也未运行会改变订单的定时任务；AI 退款审批、生产推广、备份恢复全流程未执行。
- 缺少 storage 绑定，商品服务仍提示图片上传未接入；这不是本轮 BFF/frontend 发布引入的改动。
- 临时端口转发与测试页完成验收后关闭，集群服务继续运行。

## 发布入口核实

用户反馈未在平台看到发布后，已实时查询全局 `/api/releases` 与应用 releases 列表，两条 Release 均存在。并用 Acme 管理员登录实际部署的 `/next/` 页面，在「产品 → paas-shop → 交付 → 发布」看见顶部两条 2026/10/20 15:09:08 的“已生效”记录，打开前端发布详情确认 ID、目标测试/default、镜像摘要均一致。截图见 `release-visible.png`。

本次直接调用平台 BuildRun 和 Release API，未触发 PipelineRun，因此交付中心“运行记录”不会出现本次流水线；两条 Release 的 sourceRunId 为空。发布页面与 Release 详情显示 0/1 就绪，与实时 K8s 中 BFF/frontend Deployment 均 1/1 不一致；这是另一个运行态读模型/展示同步问题，本轮仅确认并记录，未修改平台代码或重发部署。
