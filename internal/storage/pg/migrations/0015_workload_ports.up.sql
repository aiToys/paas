-- 工作负载端口字段（service 类型建 K8s Service：多微服务 DNS 互调 + 数据面服务发现前置）。
-- port = Service 对外暴露端口（>0 才建 Service）；container_port = Pod 监听端口（0 取 port）。
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS container_port INTEGER NOT NULL DEFAULT 0;
