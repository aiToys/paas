-- 回滚 0024: configcenter Namespace service_id 列。
DROP INDEX IF EXISTS idx_cc_namespaces_service;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS service_id;
