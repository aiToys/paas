-- configcenter Namespace 双 scope 模型：app（应用派生，EnsureByApp 懒建）/ shared（跨应用共享）。
-- 存量命名空间迁移为 shared。
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'shared';
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS app_id TEXT NOT NULL DEFAULT '';
UPDATE cc_namespaces SET scope='shared' WHERE scope IS NULL OR scope='';
CREATE INDEX IF NOT EXISTS idx_cc_namespaces_tenant_app ON cc_namespaces(tenant_id, app_id) WHERE app_id != '';
-- 注：并发幂等兜底复用既有 UNIQUE (tenant_id, name) 约束（0001 已有，无需新建）。
