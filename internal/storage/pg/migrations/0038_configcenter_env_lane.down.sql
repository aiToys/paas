DROP INDEX IF EXISTS idx_cclo_lookup;
DROP TABLE IF EXISTS cc_lane_overrides;
DROP INDEX IF EXISTS uq_cc_ns_tenant_app_env;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS env_id;
