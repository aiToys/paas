ALTER TABLE data_services DROP COLUMN IF EXISTS engine_id;
DROP INDEX IF EXISTS idx_engines_kind;
DROP TABLE IF EXISTS engines;
