# 部署方式与配置

| 方式 | 适用场景 | 指南 |
|---|---|---|
| Docker Compose | 本地演示、体验持久化元数据 | [快速开始](../guide/quickstart) |
| Helm 在线安装 | 可访问镜像源的 K8s | [K8s 部署](./k8s) |
| airsync 离线交付 | 私有化、气隙环境 | [离线交付](./airsync) |

## 单镜像与外部依赖

`paas-core` 同源提供四套 SPA（经典、新版、管理后台、官网）、用户文档及 API。PostgreSQL 保存元数据，K8s 承载工作负载；可观测、模型供应商、知识库与 AgentRun 执行域按需配置。

没有配置相应后端时，保存元数据、运行真实负载、采集业务数据是不同状态。安装成功不代表所有依赖已经可用。

## 生产安全配置

| 环境变量 | Helm values 字段 | 要求 |
|---|---|---|
| `PAAS_DB_URL` | `db.url`（外置）或 `db.enabled=true`（内置） | 生产持久化，空值为内存模式 |
| `PAAS_JWT_SECRET` | `auth.jwtSecret` | 至少 32 字节随机串，生产缺失或过短拒启 |
| `PAAS_SECRET_MASTER_KEY` | `security.secretMasterKey` | **64 位 hex，解码后恰好 32 字节**；生产缺失拒启 |
| `PAAS_PROD` | `auth.prod` | 生产设 `true` |
| `PAAS_COOKIE_SECURE` | `auth.cookieSecure` | 配 TLS 后设 `true`；生产未开启拒启 |
| `PAAS_DISABLE_DEMO_SEED` | `seed.demo`（反向） | 生产 env 设 `true`，或 values 设 `false` |

主密钥与 JWT secret 可分别生成，妥善保存并通过受控配置注入：

```bash
openssl rand -hex 32
```

Helm 默认面向演示，不应直接当作生产安全配置。还需设置数据库强密码、TLS 和存储；关闭 seed 不会自动删除已有数据库里的演示用户与 Key，需管理员另行核查。

## 按需启用真实能力

| 能力 | 环境变量 | Helm 配置 |
|---|---|---|
| 真实构建 | `PAAS_DEVOPS_BUILDER=k8s`、`PAAS_REGISTRY` 等 | `devops.builder`、`devops.registry` 及访问凭证 |
| 指标 / 日志 / 链路 | `PAAS_PROM_URL` / `PAAS_LOKI_URL` / `PAAS_JAEGER_URL` | `observability.prometheusURL` / `lokiURL` / `jaegerURL` |
| Core 自身 trace | `PAAS_OTEL_ENDPOINT` | `observability.otelEndpoint`，OTLP/HTTP 地址，不含 scheme |
| 模型网关示例 | `PAAS_AIROUTER_BASE_URL`、`PAAS_AIROUTER_API_KEY` | `maas.airouterBaseUrl`、`maas.airouterApiKey`；也可在后台管理模型/通道/凭证 |
| 知识库 | `PAAS_KB_QDRANT_URL`、`PAAS_KB_MINIO_ENDPOINT` 等 | `kb.*`，另需可用 embedding 模型 |
| 智能研发执行域 | `PAAS_AGENTRUN_URL`、`PAAS_AGENTRUN_TOKEN` | `agentrun.url`、`agentrun.token` |
| 容器终端 | `PAAS_TERMINAL_ENABLED=true` | `workload.terminalEnabled=true`；另需 K8s exec 权限与用户 `workload:exec` |

builder 默认 mock；三支柱分别配置，不应把未接入或空查询解释成真实健康。知识库依赖未就绪时，目录 CRUD 与上传/检索的可用性也不同。

常见问题见 [K8s 检查](./k8s)、[服务运行](../features/runtime)和[平台诊断](../admin/)。
