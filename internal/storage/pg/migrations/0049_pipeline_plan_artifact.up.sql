-- 0049: 研发交付闭环——执行计划与产物锁定（spec 2026-10-17 §6/§7）
-- plan_hash/template_*: preview 与 trigger 一致性 + 模板身份快照（触发后不可变）。
-- artifact: 触发时锁定的候选产物五元组（imageSource=latestReady 预解析固化）。
-- decisions: 结构化决策记录（manual-test/approve/canary，append-only）。
-- stage_runs.checks: 结构化检查结果（smoke 探活目标/预期/实际/降级说明）。
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS plan_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS template_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS template_version INT NOT NULL DEFAULT 0;
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS artifact JSONB;
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS decisions JSONB;
ALTER TABLE stage_runs ADD COLUMN IF NOT EXISTS checks JSONB;
