# 数据服务

平台以六类 Kind 管理数据服务；可用引擎、镜像与实际能力取决于环境配置：

| Kind | 引擎示例 | 连接注入 |
|------|---------|---------|
| db | PostgreSQL / MySQL | `DATABASE_URL` |
| cache | Redis | `REDIS_URL` |
| mq | NATS | `NATS_URL` |
| storage | MinIO | S3 兼容端点 |
| vector | Qdrant | `QDRANT_URL` |
| search | Meilisearch | `MEILI_URL` |

## 三种供给模式

- **managed**：平台拉起专属实例（StatefulSet + PVC 持久化，删 Pod 不丢数据）
- **external-shared**：接外部共享实例
- **external-dedicated**：接外部独占实例

## 绑定注入

应用绑定数据服务后，`BindingInjector` 按 Kind 自动写应用配置连接条目；解绑自动重注入剩余绑定。凭证全端点掩码，库内静态加密（AES-256-GCM）。

## 实例管理

启停 / 重启 / 扩缩容 / 升级；exporter sidecar 自动注入，业务指标按实例 label 过滤。

## 备份恢复

当前真实快照和恢复支持 PostgreSQL，需要 K8s 执行面及归档条件；其他引擎不应据此宣称具备相同恢复能力。恢复到新实例后需核验数据，见[备份与恢复](./backup)。
