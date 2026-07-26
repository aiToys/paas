# paas

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![CI](https://github.com/aitoys/paas/actions/workflows/ci.yml/badge.svg)](https://github.com/aitoys/paas/actions/workflows/ci.yml)

一站式 PaaS 平台 —— 服务治理、中间件管理、MaaS、DevOps 的基础设施统一平台。

## 状态

🚧 早期开发中。本期范围：**Platform Core 底座 + MaaS 推理平台**，其余三个子系统（服务治理 / 中间件 / DevOps）后续作为插件接入。

完整设计见 [设计规格](./docs/superpowers/specs/2026-07-26-maas-platform-foundation-design.md)，当前实施计划见 [Plan 1](./docs/superpowers/plans/2026-07-26-repo-and-core-foundation-implementation.md)。

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

```bash
make build      # 编译 core 二进制到 bin/core
make test       # 运行全部测试（含 race 检测）
./bin/core      # 运行（最小骨架，暴露 :8080/livez）
```

环境要求：Go >= 1.22、GNU make。

## 贡献

见 [CONTRIBUTING.md](./CONTRIBUTING.md)。提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)。

## 协议

[Apache License 2.0](./LICENSE) © 2026 aitoys
