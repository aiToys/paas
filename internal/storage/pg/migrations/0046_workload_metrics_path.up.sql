-- 已部署 PG 增量：workloads 表补 metrics_path 列（全新部署 0001 已含）。
-- workload spec.metricsPath 非空时 reconciler 给 Pod 注入 prometheus.io/scrape 注解（path 取该值，
-- port 取 container_port 缺省 port），Prometheus kubernetes-pods job 据此自动发现抓取。
-- 默认 '' = 不注入：多数应用不暴露 /metrics，无条件注入会产生永久 down target。
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS metrics_path TEXT NOT NULL DEFAULT '';
