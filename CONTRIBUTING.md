# 贡献指南

感谢参与 paas 项目！请先阅读本文档。

## 开发环境

- Go >= 1.26
- Node.js >= 22.13 + pnpm（前端）
- GNU make
- （可选）golangci-lint、Docker（镜像构建与本地 K8s 部署）

## 开发流程

1. Fork 仓库 → 新建分支 `feat/<short-desc>` 或 `fix/<short-desc>`
2. 本地开发，确保 `make test` 通过
3. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/zh-cn/)（`feat:` / `fix:` / `chore:` / `docs:` / `test:` / `refactor:`）
4. 推送分支并提交 Pull Request，关联对应 issue，填写 PR 模板

## 临时文件与验证产物

**本规范适用于所有开发者、AI agent、子 agent 及其调用的脚本和工具，覆盖整个仓库及所有子项目。**

- **临时文件统一放在项目根目录 `tmp/` 下**，按任务建立子目录，例如 `tmp/2026-10-21-admin-check/`；不得将排查截图、录屏、浏览器快照、调试日志、临时数据导出、试验脚本或原型散落到根目录、源码目录或文档目录。
- 无论当前工作目录位于根目录、`frontend/` 还是 `examples/`，均使用**本仓库根目录的 `tmp/`**；执行工具前创建所需目录，并显式设置输出路径，避免工具默认写入当前目录。
- 根目录 `tmp/` 由 `.gitignore` 忽略，并由 `.dockerignore` 排除出镜像构建上下文；不得使用 `git add -f` 提交其中的文件，也不提交用于保留空目录的占位文件。
- **正式交付资产与临时文件分开**：需要版本管理的文档插图、测试夹具、示例资源，以及明确纳入正式验收报告的证据，放在对应的 `docs/`、测试或资源目录中，由文档或代码引用，并确认已脱敏；不能仅为了保留临时排查文件而将其移入正式目录。
- 整理已有文件前先检查 Git 跟踪状态和引用，避免覆盖、误删或破坏链接；对仍需保留的临时文件优先迁移到 `tmp/<任务名>/`，不得擅自删除其他任务的产物。
- 每次交付前检查根目录和 `git status --short`，确认没有新增散落的临时文件、没有将临时产物暂存入库；交付时仅说明本任务产物位置，不将临时文件当作正式实现或验收通过的证明。

例如，在仓库根目录执行 `mkdir -p tmp/admin-check` 后，将浏览器截图输出到 `tmp/admin-check/overview.png`；从子目录执行工具时使用该目录的绝对路径。

## 架构约定（务必遵守）

- **业务领域逻辑不得进入 `internal/core/`**；Core 只含横切元能力。判断标准：该能力 MaaS / 治理 / DevOps 都会用吗？是→Core，否→对应插件。
- **多租户隔离由 Core 统一治理**（DB 访问层强制注入 tenant 过滤），插件不得绕过。
- **数据面与控制面解耦**：控制面只下发 CRD 期望状态，数据面组件负责实际运行。
- **Core 不依赖任何外部服务治理/中间件**（避免元设施鸡生蛋）：元数据走 PostgreSQL，事件走 Core 自带 NATS。

## 依赖与协议

- 项目协议为 **Apache 2.0**。
- **禁止引入 GPL/AGPL 等强 copyleft 依赖**；CI 会通过 `go-licenses` 校验。新增依赖前请确认其 license 兼容 Apache 2.0。
- 新增依赖后运行 `go mod tidy`。

## CI 流水线

每次 push 到 `main` 或提 PR，GitHub Actions（`.github/workflows/ci.yml`）自动跑以下 job：

| Job | 作用 |
|------|------|
| `test` | 内存后端全量测试（`-race`，零外部依赖） |
| `test-pg` | PostgreSQL 集成测试（11 模块，postgres service container，`-p 1` 串行） |
| `lint` | golangci-lint |
| `license-check` | go-licenses 拦截 GPL/AGPL 等强 copyleft |
| `build` | 编译 `bin/core`（依赖 `test`/`lint`/`test-pg` 通过） |
| `coverage` | 跑测试生成 `coverage.out` + artifact + 日志总覆盖率 |
| `frontend` | pnpm 构建三套前端 |
| `release-image` | **仅推 tag `v*` 触发**，多平台 buildx 推 `ghcr.io/aitoys/paas-core` |

- 本地复现 PG 集成测试：`make test-pg`（自动拉起 compose 的 postgres）。
- 本地复现覆盖率：`make cover`（生成 HTML 报告）。
- 发版镜像：`git tag v0.1.0 && git push origin v0.1.0` → CI 自动构建并推送 `ghcr.io/aitoys/paas-core:0.1.0` / `:0.1` / `:0`（amd64+arm64）。`release-image` 依赖全部检查 job，任一失败不发布。

## 代码风格

- 注释与文档使用**中文**（与现有代码库一致）。
- 运行 `make lint`（golangci-lint）确保无告警。
- 公开导出的函数/类型须有文档注释。
