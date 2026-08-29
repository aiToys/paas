DROP INDEX IF EXISTS idx_cc_namespaces_tenant_app;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS app_id;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS scope;
