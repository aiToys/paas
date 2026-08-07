-- Prompt 模板管理（P2）：版本化提示词，同 name 多版本行，仅一个 active。
-- variables JSONB；UNIQUE(tenant_id,name,version) 防同版本重复。
CREATE TABLE IF NOT EXISTS ai_prompts (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    name        TEXT NOT NULL,
    template    TEXT NOT NULL,
    variables   JSONB NOT NULL DEFAULT '[]',
    version     INT NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name, version)
);
-- 同 name 仅一个 active（partial unique index）
CREATE UNIQUE INDEX IF NOT EXISTS ai_prompts_active_unique ON ai_prompts (tenant_id, name) WHERE active;
