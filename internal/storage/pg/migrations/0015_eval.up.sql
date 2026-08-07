-- AI Agent 评估用例（P4）：为 Agent 定义测试用例，批量运行评分。
-- 租户私有；归属某 Agent（agent_id，无 FK 软关联--删 Agent 不级联清 eval，便于追溯）。
-- (tenant_id, agent_id, name) 唯一：同 Agent 下用例名唯一便于识别。
CREATE TABLE IF NOT EXISTS ai_eval_cases (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    input       TEXT NOT NULL,
    expected    TEXT NOT NULL,
    match_type  TEXT NOT NULL DEFAULT 'contains',  -- contains | exact | regex
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS ai_eval_cases_tenant_agent_name
    ON ai_eval_cases (tenant_id, agent_id, name) WHERE name <> '';
CREATE INDEX IF NOT EXISTS ai_eval_cases_agent ON ai_eval_cases (tenant_id, agent_id);
