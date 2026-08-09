-- 回滚 0025: pipeline_templates version 列。
ALTER TABLE pipeline_templates DROP COLUMN IF EXISTS version;
