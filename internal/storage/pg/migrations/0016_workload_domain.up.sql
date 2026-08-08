-- 已部署 PG 增量：workloads 表补 domain 列（全新部署 0001 已含）。
-- workload spec.domain 非空时 reconciler 自动建 Ingress（host=domain -> Service:port）。
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS domain TEXT NOT NULL DEFAULT '';
