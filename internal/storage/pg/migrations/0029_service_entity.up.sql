-- 服务实体（应用→服务→环境 三层心智，spec 2026-08-19 Phase 1）
CREATE TABLE IF NOT EXISTS services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    repo_id    TEXT NOT NULL DEFAULT '',
    repo_path  TEXT NOT NULL DEFAULT '',
    port       INTEGER NOT NULL DEFAULT 0,
    replicas   INTEGER NOT NULL DEFAULT 0,
    build_args JSONB,
    env        JSONB,
    model_ref  TEXT NOT NULL DEFAULT '',
    tools      JSONB,
    schedule   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_name ON services(tenant_id, app_id, name);
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS service_id TEXT NOT NULL DEFAULT '';
