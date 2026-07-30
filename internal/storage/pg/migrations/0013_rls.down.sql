DROP POLICY IF EXISTS env_tenant_isolation ON environments;
ALTER TABLE environments DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS ds_tenant_isolation ON dataservices;
ALTER TABLE dataservices DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS users_tenant_isolation ON users;
ALTER TABLE users DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS wl_tenant_isolation ON workloads;
ALTER TABLE workloads DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS apps_tenant_isolation ON applications;
ALTER TABLE applications DISABLE ROW LEVEL SECURITY;
