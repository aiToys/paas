-- AI Agent（P3）：命名预设，组装 system prompt + 工具 + KB RAG 调底层 LLM。
-- 租户私有；tools/knowledge_bases/skills JSONB（引用 ID 列表）；name 租户内唯一。
-- skills 列由 0031 增量补（已部署实例），此处合并 schema（全新部署一并建）。
CREATE TABLE IF NOT EXISTS ai_agents (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL,
    system_prompt   TEXT NOT NULL DEFAULT '',
    prompt_ref      TEXT NOT NULL DEFAULT '',
    tools           JSONB NOT NULL DEFAULT '[]',
    knowledge_bases JSONB NOT NULL DEFAULT '[]',
    max_steps       INT NOT NULL DEFAULT 5,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    skills          JSONB NOT NULL DEFAULT '[]',
    UNIQUE (tenant_id, name)
);
