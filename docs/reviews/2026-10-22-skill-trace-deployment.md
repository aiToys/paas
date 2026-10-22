# Skill Trace：K8s 部署与真实客服验收

时间：2026-10-22 22:41–23:02（Asia/Shanghai）。用户要求“部署到 k8s 测试完成后提交”；执行顺序为源码快照、构建、部署、真实测试、页面验收、最后提交。

## 制品与来源

- 分支：`console-v2`；构建基线 `4d05819a7063febfef2ac616bab7eeaf96e8aeb5` 加本次未提交变更。
- 构建输入：1379 个正式文件，源码快照 SHA-256 为 `c1d6a6f24f9cd694f4583d11f2c0f33210c9176e2523039c4fd3dc6e83af0c30`，写入镜像标签 `io.onepaas.source.worktree-sha256`。
- 快照覆盖跟踪文件及本次正式新增源码，排除根目录 `docs/` 和 `AGENTS.md`；前端文档源仍在快照内。构建前、部署前和提交前核对制品输入未变；根目录报告在验收后补充。
- 镜像：`192.168.41.122:30050/paas/paas-core:skill-trace-c1d6a6f24f-20261022`。
- Registry 与 Pod 的实际摘要一致：`sha256:15be627ea4773348230761ffb54af6b33d8720d27f495276a050c5afb9ff87d8`。

使用仓库 Dockerfile 完整构建各套前端、文档及 linux/amd64 Core。按用户要求测试完成后才提交 Git；不把基线提交号称为已包含新增功能的提交号。

## 部署结果

目标为 `kubernetes-admin@kubernetes`，namespace `paas`，release `paas`。Helm revision 390 → **391**，状态 `deployed`；`paas-core-6fbb9477d-8lnsq` 1/1 Ready，重启 0 次。

部署前保存受限权限配置与资源快照，渲染 11 份资源，仅 `Deployment/paas-core` 的镜像变化。构建后复核 Helm 值及 Deployment spec 未被其他操作修改，再通过直接 `helm upgrade --reuse-values` 升级；部署后全部计算值仅 `image.tag` 变化。本次不是内置 PaaS 流水线发布；未执行部署脚本的额外基础设施 apply、CRD 更新、强制重启或镜像清理。

数据库部署前后均为迁移 59、`dirty=false`，没有新增迁移。平台 `/livez` 正常。入口返回的新版控制台全部 154 份 HTML/JS/CSS 与同一 Docker 构建阶段导出的 dist 逐字节一致。

## 真实业务测试

通过 [测试商城](http://shop.test.k8s.dd/) 的真实 `/api/chat` 入口，覆盖 ingress → BFF → chatbot → Core → 已配置供应商。入口元数据确认环境为 `env-acme-test`、应用 `app-1792553215498804989`，没有使用生产商城入口。每个用例使用独立合成会话 ID，未读取其他用户会话。

保留原 `shop-agent`、`glm-5.2`、Prompt、FAQ 知识库及两项已安装 Skill，三个用例前后配置逐字段一致。测试仅查询真实商品与已有测试订单，以及要求跳过审批退款以核对拒绝；不下单、不支付、不退款、不修改业务配置。

| 场景 | 实际结果 | Trace 证据 |
|---|---|---|
| 商品查询 | `query_product` 查询商品 5，回答 USB-C 拓展坞、199 元、库存 300，与测试数据一致；SSE 正常结束，无错误帧 | 2 个 Skill 加载、2 个 LLM、1 个工具调用 |
| 订单查询 | `query_order` 查询已有测试订单 `ORD-0779425486`，回答已付款、299 元及商品信息；SSE 正常结束，无错误帧 | 2 个 Skill 加载、2 个 LLM、1 个工具调用 |
| 退款拒绝 | 明确拒绝跳过审批直接退款，说明客服没有退款执行权限；没有执行工具或写操作 | 2 个 Skill 加载、1 个 LLM、0 个工具调用 |

三条实际链路：

- [商品查询](http://paas.k8s.dd/next/observability?tab=traces&traceId=7ea81f69b435717dd6a821dbb712c414)
- [订单查询](http://paas.k8s.dd/next/observability?tab=traces&traceId=822d42e975136604fdf37ba12e06ae14)
- [退款拒绝](http://paas.k8s.dd/next/observability?tab=traces&traceId=fbf0c0188152d0978f41b7a8999f9bd6)

## 链路与页面验收

平台经既有 Jaeger 后端读取到实际链路：商品与订单各 11 个 Span，退款 9 个 Span；服务包括 `paas-shop-bff`、`paas-shop-chatbot`、`paas-core`。每条链路都包含同一 Agent 的两个 `paas.skill.load`，父子关系正确，实际指令 SHA-256 与对应 Skill 当前正文逐项一致，状态 loaded、加载计数 2、上下文准备完成。

五次真实 `provider.chat` 均上报 `glm-5.2` 和正值输入/输出 Token 用量；按 Span 汇总为输入 4714、输出 647，不将该汇总视作账单对账。实际工具 Span 上报 `execute_tool` 操作和模型提供的调用 ID，三个用例都没有 `refund_order` 调用。

浏览器通过 kubectl 本机安全隧道加载实际部署的可观测页面，核对两项技能、内容哈希、来源、副本更新时间以及“来源模板版本未提供”；没有为演示自建技能推测模板版本。点击“查看加载 Span”成功选中对应真实节点，父 ID 与 Agent 节点一致。该浏览器验收是实际部署页面；外部入口的静态制品另经上述 154 份文件比对确认。

首次请求前测试 BFF 的本机转发断开，没有发出模型请求；随后改从真实测试域名完成全部三次对话。第三条链路首次读取时仅 Skill 子 Span 先导出，父 Span 和 LLM Span 尚未到齐；复用已完成的对话结果重查，等待异步导出完整后通过，没有额外重跑模型请求。

## 边界与留存

- 本次验证 Skill 加载证据、真实模型/只读工具、跨服务 Trace、页面与制品；不代表模型在所有输入下遵循技能，也不代表退款审批、业务幂等和供应商账单完成全面验收。
- 本次同一 Trace 中的服务为上述三个。工具耗时可见，未将 MCP 下游、数据库或向量存储的完整内部 Span 计为通过；当前页面未返回关联日志，也未将日志联动算作通过。
- Trace 保留受现有 Jaeger 保留策略影响；链接不是永久审计归档。
- 本地全量 race、lint、前端检查与测试见实现记录；本次没有触发远端 CI，也没有 Git push、打 tag 或发布 GitHub release。

受限配置快照、源码哈希清单、构建日志、三次 SSE、真实 Trace JSON、逐文件比对与浏览器截图均在根目录 `tmp/2026-10-22-skill-trace-deploy/`，不纳入 Git。临时隧道在验收后关闭。
