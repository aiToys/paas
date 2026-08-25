-- AI Skill：可复用指令能力包，Agent 绑定后运行时注入 system prompt（与 Prompt 互补：
-- Prompt 是整体 system prompt 模板；Skill 是可叠加的能力指令，一个 Agent 可绑多个组合）。
CREATE TABLE IF NOT EXISTS ai_skills (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    instructions TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);

-- Agent 绑定 skills（引用 ID 列表，与 tools/knowledge_bases 同款 JSONB）。
ALTER TABLE ai_agents ADD COLUMN IF NOT EXISTS skills JSONB NOT NULL DEFAULT '[]';
