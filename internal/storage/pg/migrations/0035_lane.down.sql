DROP POLICY IF EXISTS lanes_tenant_isolation ON lanes;
DROP TABLE IF EXISTS lanes;
ALTER TABLE applications DROP COLUMN IF EXISTS resource_template;
ALTER TABLE workloads DROP COLUMN IF EXISTS resources;
