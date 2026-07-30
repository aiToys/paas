-- 0013: 行级安全（RLS）渐进式启用。
-- POLICY 语义：tenant_id = current_setting('app.tenant_id', true)
--   - 未设 app.tenant_id 的会话 → current_setting 返 NULL → 条件 NULL → 放行（不破坏现有查询层过滤路径）
--   - 已设 app.tenant_id 的会话 → 数据库强制按租户过滤（纵深防御，绕过查询层也安全）
-- 这是"安全网"模式：查询层仍强制 tenant 过滤（现状），RLS 作第二道防线。
-- 完整接入（force 所有用查询层 set）归后续；本迁移对核心业务表启用机制。
-- 其余业务表（api_keys/dataservices/code_repos/...）结构同构，按需追加。

ALTER TABLE applications ENABLE ROW LEVEL SECURITY;
CREATE POLICY apps_tenant_isolation ON applications
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE workloads ENABLE ROW LEVEL SECURITY;
CREATE POLICY wl_tenant_isolation ON workloads
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_tenant_isolation ON users
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE dataservices ENABLE ROW LEVEL SECURITY;
CREATE POLICY ds_tenant_isolation ON dataservices
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE environments ENABLE ROW LEVEL SECURITY;
CREATE POLICY env_tenant_isolation ON environments
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
