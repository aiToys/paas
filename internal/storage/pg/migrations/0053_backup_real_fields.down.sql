ALTER TABLE backups
  DROP COLUMN IF EXISTS mode,
  DROP COLUMN IF EXISTS engine,
  DROP COLUMN IF EXISTS provider,
  DROP COLUMN IF EXISTS artifact_ref,
  DROP COLUMN IF EXISTS size_bytes,
  DROP COLUMN IF EXISTS checksum,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS error,
  DROP COLUMN IF EXISTS source_backup_id,
  DROP COLUMN IF EXISTS restored_resource_id,
  DROP COLUMN IF EXISTS finished_at;
