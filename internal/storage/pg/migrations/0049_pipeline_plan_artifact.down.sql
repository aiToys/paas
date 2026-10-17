ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS decisions;
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS artifact;
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS template_version;
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS template_id;
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS plan_hash;
ALTER TABLE stage_runs DROP COLUMN IF EXISTS checks;
