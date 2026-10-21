# 新版交付流程 P0 实现与验证

日期：2026-10-20。范围：`/next/` 用户控制台、流水线构建参数、部署状态读取。

## 已实现

- 产品交付页提供“构建 / 部署已有镜像 / 构建并部署”；服务列表和运行抽屉复用同一个交付弹窗。
- 三个入口分别绑定 `tpl-delivery-build`、`tpl-delivery-deploy`、`tpl-delivery-build-deploy`，创建真实 PipelineRun，完成受理后直达运行详情。首次预览会按需创建产品的标准流水线绑定；预览不会执行构建或部署。
- 显式选择服务、仓库、分支、环境、泳道、镜像。已有环境浏览上下文不会替代执行目标。泳道只列目标环境的活跃实体及 default 基线。
- 支持 Dockerfile、构建目录与字符串构建参数；从服务带入 buildArgs，从仓库带入构建配置。
- 内置 Gitea 仓库可在预览时解析分支 HEAD；外部仓库需要填写完整提交 SHA。SHA 进入阶段快照及 planHash，修改目标后必须重新预览。标准模板的运行接口要求提供预览 hash；目标漂移返回 409。
- K8s 与进程构建器均检出锁定提交并校验 HEAD，修复“标签为旧提交但代码为分支最新 HEAD”的情况。未指定提交的兼容路径仍从 HEAD 构建。
- 已有镜像预览展示 imageId/digest，并记录来源仓库、提交、BuildRun；拒绝不存在、未就绪或不属于当前应用的镜像。
- 交付中心与产品交付页统一展示 PipelineRun、BuildRun、Release，保留独立调用的历史，不为旧记录虚构流水线。可按类型、产品、状态与关键词过滤，保留原 URL 查询筛选和详情深链。
- 修复工作负载详情的值拷贝问题，返回状态读取器回填后的 Ready/Status。
- 等待就绪时若状态读取失败即报告失败，避免使用存储中的过期 Ready 值宣布成功。
- 修复重复测试发现的中止竞争：内存及 PG 存储拒绝用旧快照覆盖 aborted 终态；阶段开始时若已中止，不再发起该阶段的构建或部署副作用。
- Release 的 succeeded 展示为“配置已更新”；运行就绪与业务验收分别展示。保留独立回滚接口及其真实记录。

## 边界与兼容

- 复用现有 pipeline CRUD、preview、run、BuildRun、Release 和 Lane API，无新增外部服务、依赖、数据库迁移或根模块业务逻辑。
- 三种标准模板只有 build/deploy 阶段，不隐式提升环境、打版本或合并 main。原自定义 CI/CD/Agent 模板与直接调用 API 保留。
- 平台模板随 Core 启动幂等 seed。使用新入口前需同时部署包含这些模板的新版 Core 和 `/next/` 前端。
- 原应用成员、租户隔离、pipeline:write、prod:write、生产资源规格等后端校验继续生效。标准模板部署阶段等待副本就绪；没有将副本就绪等同于业务验收。
- 定时任务沿用专用工作负载流程，本次服务交付弹窗不接受 cron 服务。
- 已完成实现、本地验证与测试集群发布。没有向远程提交或推送；发布使用基线加本次范围的隔离源码，不包含工作区独立的后台管理改动。平台升级和真实流水线证据见 [部署记录](deployment.md)。

## 本地验证证据

- `go test -race ./internal/devops/pipeline ./internal/devops/builder ./internal/workload ./cmd/core`。
- `pnpm --dir frontend/console-user-v2 build`：TypeScript + Vite 通过。已有 theme-init 脚本、其他页面 CSS @import 顺序和依赖注释的非阻断警告仍存在。
- 修改的 v2 文件定向 ESLint 通过；本次范围 `git diff --check` 通过。
- 审批后中止与中止终态保护的竞态测试连续运行 20 次通过。
- `pnpm --dir frontend/console-user-v2 test:ux`：17 项通过，覆盖独立部署历史、构建来源关联和实际 startedAt 排序。
- 新增 Go 回归：详情读取真实就绪数；标准模板必须预览；预览后更改提交被拒；前端构建参数透传；选定镜像归属和来源锁定；数值端口不被误报缺失；数据面状态不可读时不误报成功；中止后陈旧阶段不得复活运行或继续执行。
- 构建器测试使用真实临时 Git 仓库和假的 Docker：分支移动后仍构建已确认提交；找不到锁定提交时不执行 Docker。这不证明真实镜像仓库 push 或 K8s 构建成功。

浏览器使用隔离内存后端，明确设置 `PAAS_K8S_ENABLED=false`、空 DB/Gitea/AgentRun，构建器为 mock。页面验证完成：标准构建 → PipelineRun 详情；修改 Dockerfile → 预览失效；显式测试环境 → 活跃泳道选择 → digest 确认；交付中心同时展示流水线、关联构建和独立部署。测试数据随本地进程退出消失。

- [统一交付历史截图](history-local.png)
- [执行目标预览截图](target-preview-local.png)

**集群验证补充**：新版 Core 已发布到测试集群（Helm revision 379）；通过新页面完成 BFF 的真实 Gitea → K8s Job → registry → Release → 工作负载就绪链路，运行 `run-9f6f1cbd2a62`。同提交重构建得到相同 digest，未发生新的 Pod 滚动替换。详情见 [部署记录](deployment.md)。

**尚未验证**：独立 PostgreSQL 集成测试、不同 digest 的滚动替换、此次新入口的完整业务写入/订单/支付流程、生产发布。真实集群使用 PG，不代替专门的 PG 回归测试。

本地验证结束后已关闭临时浏览器页及本次启动的 Core/Vite 进程，保留工作区原有服务。
