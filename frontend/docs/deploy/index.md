# 部署方式

| 方式 | 适用 | 入口 |
|------|------|------|
| Helm 公网安装 | 有外网的 K8s | `helm install paas deploy/charts/paas` |
| 离线交付（airsync） | 私有化 / 气隙环境 | `airsync bundle` → 介质 → `airsync install` |

## 单镜像架构

`paas-core` 单镜像同源 serve：

- 前端三套 SPA + 本文档站（go:embed 进二进制）
- 全部 API
- 依赖：PostgreSQL（元数据）+ 可选 Prom / Loki / Jaeger（可观测三支柱，env 开关独立混用）

## 环境变量速查

| 变量 | 必填 | 说明 |
|------|------|------|
| `PAAS_DB_URL` | 生产是 | PostgreSQL DSN（空 = 内存模式，仅 dev） |
| `PAAS_SECRET_MASTER_KEY` | 生产是 | 敏感数据静态加密 master key（≥32 字节） |
| `PAAS_PROD` | 生产是 | `true` 时强制安全基线 |
| `PAAS_COOKIE_SECURE` | 生产是 | TLS 部署后开启 |
| `PAAS_DISABLE_DEMO_SEED` | 生产是 | 关闭演示凭证 / 示例数据 |
| `PAAS_PROM_URL` / `PAAS_LOKI_URL` / `PAAS_JAEGER_URL` | 否 | 可观测三支柱后端地址 |
| `PAAS_OTEL_ENDPOINT` | 否 | 控制面自身 trace 上报 |

详见 [K8s 集群部署](./k8s) 与 [离线交付](./airsync)。
