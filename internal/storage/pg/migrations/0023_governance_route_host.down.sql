-- 回滚 0023: governance Route host 列。
ALTER TABLE gov_routes DROP COLUMN IF EXISTS host;
