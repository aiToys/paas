# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目定位

一站式 PaaS 平台 —— 面向基础设施的统一平台，覆盖服务治理、中间件管理、MaaS、DevOps。

**当前阶段**：开源就绪起步期。本期范围 = **Platform Core 底座 + MaaS 推理平台**，其余三个子系统（治理/中间件/DevOps）后续作为插件接入。完整设计见 `docs/superpowers/specs/2026-07-26-maas-platform-foundation-design.md`。

## 关键架构约束（已定，勿轻易推翻）

- **主语言 Go**，云原生控制面。
- **混合部署**：控制面跑在 K8s 上，数据面既能纳管 K8s 原生资源，也能接管外部实例。
- **商业多租户**；**SaaS + 私有化双模交付**（控制面必须可打包为离线交付件）。
- **三层 + 插件架构**：Platform Core（最小不可分内核）+ 插件化子系统。子系统是**插件而非独立微服务**。
- **数据面与控制面解耦**：控制面只下发 CRD 期望状态，控制面挂了数据面继续跑。
- **Core 不依赖任何外部服务治理/中间件**（避免元设施鸡生蛋）：元数据 PostgreSQL，事件 NATS（Core 自带）。
- **全开源，Apache 2.0**；CI 禁止引入 GPL/AGPL 依赖。

## 技术栈

| 领域 | 选型 |
|------|------|
| 控制面 | Go + controller-runtime + kubebuilder |
| 元数据存储 | PostgreSQL |
| 事件总线 | NATS（嵌入式） |
| 推理引擎 | vLLM（纳管，不自研） |
| GPU 调度 | K8s device-plugin + 自研编排（本期仅显存核算 + 反亲和） |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo |
| 交付 | Helm + OCI + `airsync` 离线工具 |
| 前端 | Vue 3 + Element Plus + Vite + TypeScript + Pinia |
| 后台前端基座 | Fork `vue-admin/vue-admin`（MIT） |
| API 契约 | OpenAPI + codegen（前后端类型一致） |

## 仓库结构（monorepo）

```
cmd/core/                       # Platform Core 启动入口
pkg/plugin/                     # 插件契约（对外可见）
internal/core/                  # Core 内部：plugin/identity/health
frontend/                       # pnpm workspace（三套前端）
├── console-admin/              # 后台管理（基于 vue-admin 基座，MIT）
├── console-user/               # 用户控制台（模型市场/部署/Playground/API Key/用量）
└── landing/                    # 官网展示页
docs/superpowers/{specs,plans}/ # 设计与实施计划
CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md LICENSE(.gitignore 后续子模块: deploy/ airsync)
```

## 常用命令

**后端（根目录）：**

```bash
make build          # 编译 bin/core
make test           # go test ./... -race
make lint           # golangci-lint run ./...
./bin/core          # 运行，暴露 :8080（/livez /v1/models /v1/chat/completions）
go test ./internal/core/gateway/ -run TestChatCompletions -v   # 单个测试
PAAS_API_KEY=sk-xxx ./bin/core   # 自定义 API Key
```

端到端验证（core 启动后）：

```bash
# 列模型
curl -H "Authorization: Bearer sk-paas-dev-key" http://localhost:8080/v1/models
# 流式推理
curl -N -H "Authorization: Bearer sk-paas-dev-key" -H "Content-Type: application/json" \
  -d '{"model":"echo","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
```

**前端（`frontend/` 目录）：**

```bash
pnpm install                  # 安装三套全部依赖
pnpm dev:admin | dev:user | dev:landing   # 分别启动（端口 5173/5174/5175）
pnpm build                    # 构建三套
```

## 垂直切片（已落地）

MaaS 端到端最小闭环已打通，验证了插件契约与数据流：

```
console-user Playground → Gateway(OpenAI 兼容 SSE + API Key 鉴权 + Token 计量)
                          → MaaS 插件(进程内编排) → echo Provider(回显) → 流式返回
```

- `pkg/provider/`：Provider / GatewayRegistrar 平台级公共契约（独立包避免 import 循环）。
- `internal/core/gateway/`：API Gateway（路由表 / API Key 中间件 / OpenAI 流式 handler / Meter）。
- `internal/maas/`：MaaS 插件（实现 `plugin.Plugin`，Init 阶段注册 echo provider）+ echo provider。
- `pkg/plugin.CoreDeps` 新增 `Gateway()` 注入点（依赖倒置）。
- 切片**不依赖 K8s/GPU**（进程内）；真实 vLLM 与 K8s 编排为下一切片。

## 前端架构

三套独立 SPA，共享设计系统（Element Plus + 暗黑模式）：

| 应用 | 定位 | 端口 |
|------|------|------|
| console-admin | 平台运维/管理员（基于 `vue-admin` 基座，RBAC/JWT/动态路由已就绪） | 5173 |
| console-user | 租户开发者（模型市场/部署/Playground/API Key/用量计费） | 5174 |
| landing | 访客官网展示页（静态、SEO 友好） | 5175 |

- API 契约：后端 OpenAPI 自动生成前端 TS 类型（Plan 4 起接入 Gateway）。
- console-admin 的基座源码自带其 `CLAUDE.md` 与 `docs/standards/`（四层架构 lib/app/modules/shared），改它时遵循其自身规范。

## 开发约定

- 新建模块或引入新技术栈时，同步更新本文件对应章节。
- 注释语言与代码库现有注释保持一致。
- **未经用户明确要求，不要执行 git commit / 分支操作。**
- 所有依赖须与 Apache 2.0 兼容；新增依赖前确认 license。
- 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"
- 多租户隔离由 Core 统一治理（DB 访问层强制 tenant 过滤），插件不得绕过。
