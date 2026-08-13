DROP INDEX IF EXISTS idx_wl_service;
ALTER TABLE workloads DROP COLUMN IF EXISTS service;
