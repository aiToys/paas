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
