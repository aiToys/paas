# AI 助手 K8s 部署记录

日期：2026-10-28（Asia/Shanghai）。

## 部署结果

- 集群：`kubernetes-admin@kubernetes`，namespace/release：`paas`。
- 使用直接 Helm 升级，未运行 `scripts/deploy-k8s.sh`，未触发平台内部流水线。
- Helm revision：`404 → 405`，状态 `deployed`。
- 镜像：`192.168.41.122:30050/paas/paas-core:assistant-20261028-v2`。
- 推送与 Pod 实际 digest 一致：`sha256:27c57e45425fed85edbe0f26ce65da44228945209df0c88c6bed75fbc980ffb5`。
- Pod：`paas-core-6f655ddf9c-5fcmp`，`1/1 Ready`、零重启。
- 入口：`http://paas.k8s.dd/next/`，顶栏 AI 助手。
- 构建基于当前 `console-v2` 工作区（含此前未提交修改），基础 HEAD 为 `7a9f8170e2683aa218b831d10f2917a56f9380a0`；未执行 commit/push。
- `--reuse-values` 保留集群现有配置，升级前后 JSON 比对仅 `image.tag` 改变。

## 数据库安全与迁移

原 `0061` 包含关闭助手三表 RLS 的操作，自动审批因此拦截首次升级，未执行该版本。随后移除关闭 RLS 的操作，重新构建为 `v2`，并在独立临时 PostgreSQL 上增加、通过非特权角色隔离测试，再执行升级。

- 线上迁移版本 `60 → 61`，`dirty=false`。
- 助手三表 `relrowsecurity=true`，原租户 policy 保留。
- 原有 11 个会话、15 次任务保留，迁移前无同会话活动任务冲突。
- 部署前保存完整 PostgreSQL 备份及 Helm 配置，存放在忽略目录 `tmp/assistant-deploy/`，敏感备份仅属本机临时恢复材料。
- 现有 Core 数据库角色原本是超级用户，未因本次部署新增或扩大权限。RLS 对该角色不生效，用户侧隔离仍依赖强制 tenant/user 授权及 SQL 过滤；本次验收不代表数据库最小权限改造已经完成。

## 已完成验证

- 相关后端包 race 测试通过，`make lint` 为 0 issues。
- 独立 PostgreSQL 4 项集成测试通过，包含迁移后保留 RLS、非特权角色缺少租户参数不可读、跨租户不可读、对话恢复和 lease fencing。临时容器已清理。
- Docker 完整前端与 Go 编译通过，镜像推送和真实 Pod digest 核对通过。
- 线上只读接口：持久化设置、官方只读 Agent 可用、旧会话恢复、稳定消息 ID、跨租户及同租户其他用户返回 404，通过。
- `/next/` HTML、引用的静态资源和消息 OpenAPI 契约通过。
- 浏览器真实控制台：打开助手恢复旧问答、历史列表切换、关闭重开保留会话，通过。页面展示来自部署前真实历史，不冒充本次新模型调用。

## 尚未完成 → 已完成（同日补验）

首次验收脚本曾被自动审批拦截（数据传输授权未确认）。用户确认后补验，脚本 `tmp/assistant-deploy/real-acceptance.sh`，结果 15/15 全绿：

- **investigate 真实诊断**：completed，quality=sufficient，证据 10 条；自然语言回答引用真实取证（1/1 Ready、0 重启、资源余量）。
- **真实用量口径**：`usageSource=provider tokens=17795` —— 供应商返回的真实 token 计量已落库，与 estimated 口径显式区分。
- **explain 追问**：completed，回答明确引用历史证据与取证时间（「根据 2026-10-28 11:15:19 的单次快照，内存 working set 约 16MiB」），确认基于已有证据不重新取证。
- **连续消息流**：`GET sessions/{id}/messages` 恢复 user=2 / assistant=2 两轮完整问答。
- **会话任务列表**：runs=2（历史入口数据源）。
- **隔离回归**：跨租户读消息 404。
- **前端入口**：`/next/assistant`、`/next/` 均 200。

供应商推理质量（结论正确性）与异常进程退出期间的费用对账仍属后续观察项，不阻塞本模块验收。

临时日志、脚本、截图与备份统一位于根目录 `tmp/assistant-deploy/`，不纳入 Git。
