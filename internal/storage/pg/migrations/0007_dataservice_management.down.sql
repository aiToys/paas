ALTER TABLE data_services DROP COLUMN IF EXISTS source;
ALTER TABLE data_services DROP COLUMN IF EXISTS image;
ALTER TABLE data_services DROP COLUMN IF EXISTS storage_gb;
ALTER TABLE data_services DROP COLUMN IF EXISTS memory;
ALTER TABLE data_services DROP COLUMN IF EXISTS cpu;
ALTER TABLE data_services DROP COLUMN IF EXISTS replicas;
