-- 0024: configcenter Namespace 加 service_id 列（关联 governance Service）。
-- 空表示不关联服务（向后兼容）；非空表示该 namespace 的配置归属于指定服务，
-- governance Service 详情页可聚合显示关联配置（双向显示）。
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS service_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_cc_namespaces_service ON cc_namespaces(service_id) WHERE service_id <> '';
