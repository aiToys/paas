-- AI 编排广场：跨租户共享能力市场（发布 = 脱敏快照落库，安装 = fork 副本到本租户）。
-- 平台级公开（无 tenant 过滤，同 maas 模型目录先例）；快照不可变（源实体后续修改不影响）。

-- 既有实体补广场元数据字段。
ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS use_cases TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS examples TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS installed_from TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_prompts ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_prompts ADD COLUMN IF NOT EXISTS installed_from TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_tools ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_tools ADD COLUMN IF NOT EXISTS installed_from TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_agents ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_agents ADD COLUMN IF NOT EXISTS installed_from TEXT NOT NULL DEFAULT '';

-- 广场条目（快照 JSONB 按 entityType 反序列化；同发布者同类型同名唯一，重发布覆盖）。
CREATE TABLE IF NOT EXISTS marketplace_items (
    id               TEXT PRIMARY KEY,
    entity_type      TEXT NOT NULL,              -- skill | prompt | tool | agent
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    category         TEXT NOT NULL DEFAULT '',
    snapshot         JSONB NOT NULL,             -- 脱敏后的完整实体快照（不可变）
    publisher_tenant TEXT NOT NULL,
    publisher_name   TEXT NOT NULL DEFAULT '',
    installs         INT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entity_type, name, publisher_tenant)
);
