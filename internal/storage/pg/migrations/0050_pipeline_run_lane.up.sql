-- run.Lane：触发时从最终解析 stages 提取的部署泳道（关闭保护真实目标关联，spec 2026-10-17 L5）。
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS lane TEXT NOT NULL DEFAULT '';
