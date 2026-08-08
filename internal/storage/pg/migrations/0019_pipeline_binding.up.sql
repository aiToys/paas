-- Pipeline 模型改「模板+绑定」：stages 列保留（旧数据兼容，不再读写），加 param_overrides。
-- stages 不再在 pipeCols 中（SELECT/INSERT/UPDATE 改用 param_overrides），保留列避免影响历史数据。
ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS param_overrides JSONB NOT NULL DEFAULT '{}';

-- 清理测试残留 Pipeline（111/222 系列 per-app 复制模型数据，重构后改默认绑定自动创建）。
DELETE FROM pipelines WHERE name LIKE '111-%' OR name LIKE '222-%';
