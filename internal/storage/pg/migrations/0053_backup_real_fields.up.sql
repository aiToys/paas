-- 0053: 备份真实快照字段（spec 2026-10-17 专项05 阶段A/B）
-- mode 三态（record 记录模式 / snapshot 真实快照 / restore 恢复作业）；
-- 存量行 mode='' 按 record 解释（回填 'record'），语义与旧「Create 即 completed」一致。
ALTER TABLE backups ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS engine text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS artifact_ref text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS size_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS checksum text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS verified_at timestamptz;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS error text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS source_backup_id text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS restored_resource_id text NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS finished_at timestamptz;
UPDATE backups SET mode = 'record' WHERE mode = '';
