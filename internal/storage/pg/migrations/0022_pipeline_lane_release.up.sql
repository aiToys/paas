-- pipeline deploy/release/lane 扩展列（Task 10）：
-- releases.lane_id/source_run_id（Task 3 引入字段补 PG schema）
-- images.version（Task 4 引入字段补 PG schema）
-- stage_runs.log（Task 1 引入字段补 PG schema）
ALTER TABLE releases ADD COLUMN IF NOT EXISTS lane_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS source_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE images ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';
ALTER TABLE stage_runs ADD COLUMN IF NOT EXISTS log TEXT NOT NULL DEFAULT '';
