# 贡献指南

感谢参与 paas 项目！请先阅读本文档。

## 开发环境

- Go >= 1.22
- GNU make
- （可选）golangci-lint、Docker、Kind（用于本地 K8s 集成测试）

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

## 代码风格

- 注释与文档使用**中文**（与现有代码库一致）。
- 运行 `make lint`（golangci-lint）确保无告警。
- 公开导出的函数/类型须有文档注释。
