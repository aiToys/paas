# 数据服务

六类同构数据服务一键开通：

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
