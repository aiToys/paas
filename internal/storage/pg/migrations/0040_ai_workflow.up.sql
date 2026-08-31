-- 智能体工作流编排（spec 2026-08-31-agent-workflow-design.md）
CREATE TABLE IF NOT EXISTS ai_workflows (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    "desc" TEXT NOT NULL DEFAULT '',
    nodes JSONB NOT NULL DEFAULT '[]',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS ai_workflow_runs (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL REFERENCES ai_workflows(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    inputs JSONB NOT NULL DEFAULT '{}',
    node_runs JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ai_workflow_runs_wf ON ai_workflow_runs (tenant_id, workflow_id, created_at DESC);

-- RLS 同款（与其余租户表一致）
ALTER TABLE ai_workflows ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ai_workflows;
CREATE POLICY tenant_isolation ON ai_workflows USING (tenant_id = current_setting('paas.tenant', true));

ALTER TABLE ai_workflow_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ai_workflow_runs;
CREATE POLICY tenant_isolation ON ai_workflow_runs USING (tenant_id = current_setting('paas.tenant', true));
