-- 0043 RLS 全表补齐（多租户纵深防御收尾）
-- 背景：真实库对账发现 60 张表中仅 21 张有 RLS；本批补齐全部「含 tenant_id 列」
-- 的 26 张租户数据表。写法对齐 0041/0042 幂等模板：
--   app.tenant_id GUC + NULL 放行（owner/迁移连接不设 GUC 时不受限，应用查询路径
--   显式 WHERE tenant_id=$1 双保险，RLS 为应用层缺陷时的兜底防线）。
-- 无 tenant_id 列的表不在此列（性质判定）：
--   tenants（租户自身，平台级）/ user_roles、api_key_roles（纯关系表，随主体过滤）
--   / stage_runs（挂 pipeline_runs，随 run 过滤）/ engines、maas_models、maas_channels、
--   maas_vendors（平台级目录）/ marketplace_items（平台级快照）
--   / application_bindings（挂 applications，经应用维度过滤）。
-- 注意：RLS 生效前提是连接角色非超级用户——dev 部署 postgres chart 默认超级用户
-- BYPASSRLS，生产部署应用独立低权限角色后 RLS 才真正拦截（见 values.yaml 注释）。

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS api_keys_tenant_isolation ON api_keys;
CREATE POLICY api_keys_tenant_isolation ON api_keys
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE app_configs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS app_configs_tenant_isolation ON app_configs;
CREATE POLICY app_configs_tenant_isolation ON app_configs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_logs_tenant_isolation ON audit_logs;
CREATE POLICY audit_logs_tenant_isolation ON audit_logs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE billing_quotas ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS billing_quotas_tenant_isolation ON billing_quotas;
CREATE POLICY billing_quotas_tenant_isolation ON billing_quotas
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE billing_records ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS billing_records_tenant_isolation ON billing_records;
CREATE POLICY billing_records_tenant_isolation ON billing_records
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE billing_usages ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS billing_usages_tenant_isolation ON billing_usages;
CREATE POLICY billing_usages_tenant_isolation ON billing_usages
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE build_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS build_runs_tenant_isolation ON build_runs;
CREATE POLICY build_runs_tenant_isolation ON build_runs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE cc_items ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cc_items_tenant_isolation ON cc_items;
CREATE POLICY cc_items_tenant_isolation ON cc_items
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE cc_lane_overrides ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cc_lane_overrides_tenant_isolation ON cc_lane_overrides;
CREATE POLICY cc_lane_overrides_tenant_isolation ON cc_lane_overrides
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE cc_namespaces ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cc_namespaces_tenant_isolation ON cc_namespaces;
CREATE POLICY cc_namespaces_tenant_isolation ON cc_namespaces
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE cc_ns_refs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cc_ns_refs_tenant_isolation ON cc_ns_refs;
CREATE POLICY cc_ns_refs_tenant_isolation ON cc_ns_refs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE cc_publishes ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cc_publishes_tenant_isolation ON cc_publishes;
CREATE POLICY cc_publishes_tenant_isolation ON cc_publishes
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE changes ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS changes_tenant_isolation ON changes;
CREATE POLICY changes_tenant_isolation ON changes
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE code_repos ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS code_repos_tenant_isolation ON code_repos;
CREATE POLICY code_repos_tenant_isolation ON code_repos
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE gov_breakers ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gov_breakers_tenant_isolation ON gov_breakers;
CREATE POLICY gov_breakers_tenant_isolation ON gov_breakers
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE gov_instances ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gov_instances_tenant_isolation ON gov_instances;
CREATE POLICY gov_instances_tenant_isolation ON gov_instances
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE gov_routes ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gov_routes_tenant_isolation ON gov_routes;
CREATE POLICY gov_routes_tenant_isolation ON gov_routes
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE gov_services ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gov_services_tenant_isolation ON gov_services;
CREATE POLICY gov_services_tenant_isolation ON gov_services
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE images ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS images_tenant_isolation ON images;
CREATE POLICY images_tenant_isolation ON images
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE integration_batches ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS integration_batches_tenant_isolation ON integration_batches;
CREATE POLICY integration_batches_tenant_isolation ON integration_batches
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE pipeline_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pipeline_runs_tenant_isolation ON pipeline_runs;
CREATE POLICY pipeline_runs_tenant_isolation ON pipeline_runs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE pipeline_templates ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pipeline_templates_tenant_isolation ON pipeline_templates;
CREATE POLICY pipeline_templates_tenant_isolation ON pipeline_templates
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE pipelines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pipelines_tenant_isolation ON pipelines;
CREATE POLICY pipelines_tenant_isolation ON pipelines
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE releases ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS releases_tenant_isolation ON releases;
CREATE POLICY releases_tenant_isolation ON releases
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE secrets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS secrets_tenant_isolation ON secrets;
CREATE POLICY secrets_tenant_isolation ON secrets
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE services ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS services_tenant_isolation ON services;
CREATE POLICY services_tenant_isolation ON services
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
