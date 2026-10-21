# OnePaaS

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![CI](https://github.com/aitoys/paas/actions/workflows/ci.yml/badge.svg)](https://github.com/aitoys/paas/actions/workflows/ci.yml)

从代码交付到服务运行，统一在一个平台完成。OnePaaS 是开源一站式平台，围绕应用、环境与服务组织 DevOps、数据服务、可观测、模型推理和智能体能力，支持多租户及 SaaS/私有化交付。

## 当前能力与边界

| 任务 | 已有能力 | 启用条件与边界 |
|---|---|---|
| 应用交付 | 产品/应用、服务、仓库、变更批次、流水线、预演、审批、发布与回滚 | 真实构建需配置 builder、Git、镜像库；默认 builder 为 mock |
| 服务运行 | Service/Job/CronJob、实例与容器日志、事件、重启/重建及操作记录 | 需要 K8s 数据面及权限；容器终端另有 feature flag |
| 泳道验证 | 流量染色、服务发现、基线回退、TTL、并行金丝雀验证 | 业务需接 SDK；数据/消息隔离需另行验证 |
| 数据与配置 | 六类数据服务、绑定注入、应用静态配置、配置中心版本与泳道覆盖 | 引擎与依赖镜像按部署配置；真实备份/恢复目前支持 PostgreSQL |
| 可观测 | 指标、日志、链路、告警与实体下钻 | 配置 Prometheus/Loki/Jaeger，并接入应用采集 |
| 模型与 AI | 模型/通道/供应商管理、聊天 SSE、embeddings、智能体、工具、知识库、工作流、资源广场 | 需可用模型通道与凭证；知识库需 Qdrant/MinIO；智能研发需独立 AgentRun 执行域 |
| 平台治理 | 多租户身份与权限、租户冻结、Key 生命周期、审计、诊断、配额与账单 | 基础配额按租户；部分计费使用模拟单价，不等于供应商对账或支付渠道结算 |

模型聚合采用第三方兼容通道，不自建 vLLM 推理集群。治理熔断目前提供规则与模拟评估，尚不能宣称真实流量驱动的数据面熔断。源码存在、本地测试、集群部署和业务验收分别提供不同证据，不以单一完成度百分比表示就绪状态。

## 控制台与文档

| 入口 | 路径 | 说明 |
|---|---|---|
| 经典控制台 | `/console/` | 应用主线与经典导航 |
| 新版控制台（试验版） | `/next/` | 应用交付、AI 平台、基础设施、组织与访问 |
| 管理后台 | `/admin/` | 平台身份、跨租户资源与运维 |
| 用户文档 | `/docs/` | 上手、操作、部署与排障 |
| API 参考 | `/api-docs` | Scalar；机器契约 `/openapi.json` |

新版“产品”对应现有 Application，与经典版应用共享后端数据，不是新增的业务层级。

## 快速开始

先克隆仓库并进入根目录。Docker/Compose 用于本地体验；首次构建需下载前后端依赖与基础镜像：

```bash
git clone https://github.com/aitoys/paas.git
cd paas
docker compose up --build
```

打开 `http://localhost:8080/console/` 或 `http://localhost:8080/next/`。演示 seed 开启时可用 `acme-admin / 123456` 登录，平台演示管理员为 `admin / 123456`；生产须关闭演示 seed 并检查已有演示凭证。

```bash
# 存活检查不需要 API Key
curl http://localhost:8080/livez
```

Compose 提供 PostgreSQL，但不会自动启用 K8s 数据面、真实构建、观测采集或模型通道。

### 源码开发

需要 Go ≥ 1.26、Node.js ≥ 22.13、pnpm、GNU make。

```bash
make build
PAAS_K8S_ENABLED=false ./bin/core
```

显式关闭 K8s，避免本机 kubeconfig 使开发写操作进入真实集群。后端未配 `PAAS_DB_URL` 时为内存模式。

在另一终端：

```bash
cd frontend
pnpm install
pnpm --dir console-user-v2 dev
```

新版访问 `http://localhost:5176/next/`；经典版 `pnpm dev:user`，访问 `http://localhost:5174/console/`。

### K8s 与离线交付

```bash
helm install paas deploy/charts/paas --namespace paas --create-namespace
```

需准备镜像、存储、集群权限与访问入口。默认 Helm values 面向演示，生产安全配置见[部署指南](./frontend/docs/deploy/index.md)；通用安装步骤见[K8s 文档](./frontend/docs/deploy/k8s.md)。`scripts/deploy-k8s.sh` 是已有开发集群的便捷脚本，需先核对开发覆盖配置。

离线流程为 `airsync bundle --version <版本>` → 介质传输 → `airsync verify` → `airsync install --bundle <文件> --target-registry <内网库>`，具体参数及镜像准备见[离线指南](./frontend/docs/deploy/airsync.md)。

## 架构与技术栈

```text
控制台 / 用户文档 / REST 与模型 API
                 ↓
Go 控制面：身份、元数据、领域模块与插件契约
                 ↓
K8s CRD → Deployment / Job / CronJob / StatefulSet
模型请求 → 第三方兼容通道
```

- Go + controller-runtime，PostgreSQL 元数据；未配置数据库可用内存模式。
- 插件契约支持拓扑排序与依赖注入，当前 MaaS 通过插件注册；其他领域按模块装配，逐步对齐插件边界。Core 事件总线接口与 NATS 方向不能视作全部实现完成。
- 已部署负载继续由 K8s 管理；平台 API、推理网关和实时发现仍依赖控制面。
- Vue 3 + TypeScript + Vite，四套 SPA（经典、新版、后台、官网）与 VitePress 文档站嵌入 Core 单镜像。
- Prometheus/Loki/Jaeger 与 OTel 按需接入；Helm/OCI 与 airsync 交付。
- `examples/` 为独立 Go module，是平台的消费者，不依赖 Platform Core 内部包。

生产需配置 JWT secret、64 位 hex 主密钥、Secure cookie/TLS、持久化数据库并关闭演示 seed。Secret 配置主密钥后加密持久化，API 掩码展示；开发明文模式不具备同等静态保护。

## 操作与开发资料

- [上手指南](./frontend/docs/guide/quickstart.md) / [核心概念](./frontend/docs/guide/concepts.md)
- [交付](./frontend/docs/features/devops.md) / [运行排障](./frontend/docs/features/runtime.md) / [模型推理](./frontend/docs/features/maas.md)
- [智能研发](./frontend/docs/features/agent-delivery.md) / [备份恢复](./frontend/docs/features/backup.md) / [平台管理](./frontend/docs/admin/index.md)
- [设计规格](./docs/superpowers/specs/) / [实施计划](./docs/superpowers/plans/)（历史设计需与当前实现对照）

测试：`make test`（全量 race）、`make test-pg`（隔离 PostgreSQL 集成测试）、`make lint`；前端使用 pnpm workspace 构建。贡献规范见 [CONTRIBUTING.md](./CONTRIBUTING.md)。

## 协议

[Apache License 2.0](./LICENSE) © 2026 The PaaS Authors
