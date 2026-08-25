-- Agent 评估历史（对标 LangSmith 评估记录：回归趋势 + 改动前后对比）。
-- 每 agent 最近 20 次由 store 惰性清理（防历史膨胀）。
CREATE TABLE IF NOT EXISTS eval_runs (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    total       INT NOT NULL DEFAULT 0,
    passed      INT NOT NULL DEFAULT 0,
    results     JSONB NOT NULL DEFAULT '[]',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_tenant_agent ON eval_runs (tenant_id, agent_id, created_at DESC);
