# paas

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![CI](https://github.com/aitoys/paas/actions/workflows/ci.yml/badge.svg)](https://github.com/aitoys/paas/actions/workflows/ci.yml)

一站式 PaaS 平台 —— 服务治理、中间件管理、MaaS、DevOps 的基础设施统一平台。

## 状态

🚧 早期开发中。本期范围：**Platform Core 底座 + MaaS 推理平台**，其余三个子系统（服务治理 / 中间件 / DevOps）后续作为插件接入。

### 已落地能力

- **MaaS 端到端闭环**：`Model → Channel → Provider` 三层抽象，OpenAI 兼容流式推理（SSE）+ API Key 鉴权 + Token 计量 + 按通道路由（优先级 + 故障降级）。当前用 mock/echo provider，真实 vLLM 为下一切片。
- **多通道模型市场**：8 个 seed 模型（富信息卡片：供应商 / 上下文 / 能力 / 定价 / 通道健康状态），OpenAI 兼容 `/v1/models` + 富信息 `/api/models`。
- **应用为主线**：应用是统一控制台主线抽象，各类资源以绑定形式归属应用（绑定/解绑 REST API 端到端）。
- **Platform Core 插件机制**：插件契约 + Registry（拓扑排序 + 环检测）+ 依赖倒置注入。

完整设计见 [设计规格](./docs/superpowers/specs/)，实施计划见 [Plans](./docs/superpowers/plans/)。

## 架构

三层 + 插件：

```
接入层   统一 API Gateway（OpenAI-compatible + 平台 REST + 鉴权/多租户路由/限流/计量）
控制面   Platform Core（最小不可分内核） + 插件槽（MaaS｜治理*｜中间件*｜DevOps*）
数据面   Inference Gateway / vLLM Pods / Provider Agent
```

- **Platform Core 最小不可分**：只含所有子系统都依赖的元能力（租户、鉴权、资源纳管、编排、可观测、插件机制）。
- **子系统是插件而非独立微服务**：以插件形式注册进 Core，共享 Core 的鉴权/存储/事件总线。
- **数据面与控制面解耦**：控制面只下发期望状态（CRD），数据面负责实际运行；控制面挂了，已部署模型继续服务。

## 技术栈

| 领域 | 选型 |
|------|------|
| 控制面 | Go + controller-runtime + kubebuilder |
| 元数据存储 | PostgreSQL |
| 事件总线 | NATS（嵌入式） |
| 推理引擎 | vLLM（纳管，不自研） |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo |
| 交付 | Helm + OCI + `airsync` 离线工具 |
| 前端 | Vue 3 + Element Plus + Vite + TypeScript |
| 协议 | Apache 2.0 |

## 快速开始

环境要求：Go >= 1.22、Node >= 22、pnpm、GNU make。

**后端**（Platform Core，暴露 :8080）：

```bash
make build && ./bin/core          # 编译并运行，默认 API Key: sk-acme-admin（Acme 租户管理员）
```

**前端**（console-user 用户控制台，:5174）：

```bash
cd frontend && pnpm install && pnpm dev:user
```

打开 http://localhost:5174 即可看到：顶栏可切换 API Key（租户/角色视角）；模型市场（8 个模型卡片）→ 点「试用」进 Playground 流式推理；应用列表 → 应用详情（资源绑定/解绑）。换 Key 即换租户，应用数据按租户隔离。

**端到端验证（core 启动后）**。API Key 绑定 (租户, 角色)：

```bash
# 模型市场富信息（平台级共享，任意有效 Key 可见）
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/models
# 流式推理（mock 通道，需 model:infer 权限）
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"qwen2.5-7b","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
# 多租户隔离：不同租户 Key 看到不同应用
curl -H "Authorization: Bearer sk-acme-admin"   http://localhost:8080/api/applications   # Acme
curl -H "Authorization: Bearer sk-globex-admin" http://localhost:8080/api/applications   # Globex
# 工作负载（应用运行形态）：跨应用按类型查询 + 扩缩容
curl -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/workloads?type=service"
curl -X PUT -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"replicas":6,"status":"running"}' http://localhost:8080/api/workloads/wl-rec-svc
```

测试与检查：`make test`（含 race）、`make lint`（golangci-lint）。

## 贡献

见 [CONTRIBUTING.md](./CONTRIBUTING.md)。提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)。

## 协议

[Apache License 2.0](./LICENSE) © 2026 aitoys
