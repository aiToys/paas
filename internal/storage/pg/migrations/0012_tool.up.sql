-- AI 工具管理（P2）：Agent 可调用的外部能力单元（MCP/HTTP/builtin）。
-- 租户私有；Config JSONB 存类型相关配置；name 租户内唯一。
CREATE TABLE IF NOT EXISTS ai_tools (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL,
    config       JSONB NOT NULL DEFAULT '{}',
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
