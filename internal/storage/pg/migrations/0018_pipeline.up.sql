-- 平台预置模板 tenant_id NULL（全租户共享）；租户自定义带 tenant_id。
CREATE TABLE IF NOT EXISTS pipeline_templates (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT,          -- NULL=平台预置
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    stages      JSONB NOT NULL DEFAULT '[]',
    builtin     BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipeline_templates_name_tenant
    ON pipeline_templates (name, COALESCE(tenant_id, ''));

CREATE TABLE IF NOT EXISTS pipelines (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    template_id TEXT NOT NULL DEFAULT '',
    stages     JSONB NOT NULL DEFAULT '[]',
    trigger    JSONB NOT NULL DEFAULT '{}',
    disabled   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_pipelines_tenant_app ON pipelines (tenant_id, app_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipelines_name_tenant_app ON pipelines (tenant_id, app_id, name);

CREATE TABLE IF NOT EXISTS pipeline_runs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    app_id        TEXT NOT NULL,
    pipeline_id   TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    commit        TEXT NOT NULL DEFAULT '',
    repo_id       TEXT NOT NULL DEFAULT '',  -- run 时解析 app 绑定的 internal CodeRepo（build stage 用）
    trigger       TEXT NOT NULL,
    trigger_ref   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    current_stage INT NOT NULL DEFAULT 0,
    version       TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_pipeline_runs_tenant_app ON pipeline_runs (tenant_id, app_id);
CREATE INDEX IF NOT EXISTS ix_pipeline_runs_pipeline ON pipeline_runs (pipeline_id, created_at DESC);
-- 同一 Pipeline 同时只允许一个 running/paused run（CI 单实例串行）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipeline_runs_active
    ON pipeline_runs (pipeline_id) WHERE status IN ('running', 'paused');

CREATE TABLE IF NOT EXISTS stage_runs (
    id           BIGSERIAL PRIMARY KEY,
    pipeline_run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    stage_index  INT NOT NULL,
    type         TEXT NOT NULL,
    name         TEXT NOT NULL,
    status       TEXT NOT NULL,
    input        JSONB NOT NULL DEFAULT '{}',
    output       JSONB NOT NULL DEFAULT '{}',
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_stage_runs_run ON stage_runs (pipeline_run_id, stage_index);
