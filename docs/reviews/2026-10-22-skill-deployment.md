# Skill 功能集群部署记录

时间：2026-10-22 19:06–19:08（Asia/Shanghai）。用户明确要求“提交部署”。

## 提交与部署结果

- 源码：`console-v2`，`45453ca9b9cc849a5ffd816bd06ad40dac57e189`，包含 Skill 模板、标准文件、控制台、演示安装器及回归测试。
- 集群：`kubernetes-admin@kubernetes`；命名空间 `paas`，Helm release `paas`。
- Helm revision：389 → **390**，状态 `deployed`；`paas-core` rollout 成功。
- 镜像：`192.168.41.122:30050/paas/paas-core:skill-45453ca-20261022`。
- Registry 与就绪 Pod 的摘要一致：`sha256:bd635a3fb247a74dda297d1bc7b78f29facdffdc21ea0075e7501fd1abe9b033`。
- Pod：`paas-core-7bc8dcd486-drtgq`，1/1 Ready，重启次数 0。
- 入口：[Skill 页面](http://paas.k8s.dd/next/ai/skills)。

采用仓库 Dockerfile 构建全部前端、文档和静态 Core 二进制，镜像为 `linux/amd64`。构建前后源码工作区保持干净。用户未要求推送 Git 分支，本次没有 Git push、打 tag 或发布 GitHub release。

## 配置与数据

升级前保存受限权限的 Helm 配置和资源快照，按当前配置渲染 11 份资源，仅 `Deployment/paas-core` 的容器镜像变化。构建后再次核对配置未被修改，使用 `helm upgrade --reuse-values` 设置独立镜像标签；升级后比较全部计算值，仅 `image.tag` 变化。未执行部署脚本中额外的基础设施 apply、CRD 更新、强制重启或镜像清理。

数据库部署前后均为迁移 59、`dirty=false`，本次没有新增迁移；启动日志未发现 panic、fatal error 或迁移失败。

## 实际验证

| 检查 | 结果与边界 |
|---|---|
| 容器构建 | 经典用户端、管理后台、新版用户端、官网、文档与 Core 构建通过。 |
| 入口健康 | `http://paas.k8s.dd/livez` 返回 200 与 `{"status":"ok"}`。 |
| 制品一致性 | 从同一 Dockerfile 的缓存 frontend 阶段导出新版控制台 dist；入口返回的全部 154 份 HTML/JS/CSS 逐字节一致。 |
| 模板目录 | 经 kubectl 本机隧道读取真实部署 API，返回六项：3 项 `instructions`、3 项 `external-agent`。 |
| 标准文件 | 六项下载均为 `text/markdown`，逐字节匹配源码 SKILL.md，SHA-256 与目录声明一致。 |
| 鉴权 | 未认证的模板目录请求返回 401。 |
| 浏览器 | 经同一本机隧道加载实际部署页面，以现有 Acme 会话打开“平台模板与对外技能”，六项内容可见；OnePaaS 接入说明展示版本、许可、场景、指令和哈希。未点击复制或修改业务配置。 |
| 本地回归 | 提交前 Go race、独立 PostgreSQL Skill 集成、安装器、前端类型/构建/UX 等证据见实现交付记录；未触发远端 CI。 |

## PaasShop 演示状态

部署前只读核查：Acme 租户的 Skill 列表为空；现有 `shop-agent` 使用 `glm-5.2`，绑定知识库及四项 MCP 工具（含 `refund_order`）。MCP 服务 `paas-shop-mcp.paas-t-acme.svc.cluster.local` 有可用端点。

本次尝试运行演示安装验收时，自动审批拒绝执行：安装器会在共享租户持久创建两项 Skill，并将现有 Agent 的工具绑定改为三项只读工具、移除退款工具；审批认为“提交部署”没有明确授权该业务配置变更。已向用户单独请求确认，等待答复；被拒绝的程序没有执行，演示 Skill 尚未安装，Agent 原绑定保持不变。

尚未验证真实模型推理、MCP 商品/订单返回事实、退款审批强制约束、离线交付或业务验收。Core 部署与模板目录通过，不等于上述业务场景通过。

临时构建日志、受限配置快照、验证 JSON 与截图保存在根目录 `tmp/2026-10-22-skill-deploy/`，Git 与 Docker 均忽略。
