-- 0027_change_management 变更管理（changes + integration_batches）。
-- 幂等（IF NOT EXISTS），与既有 migration 风格一致。
CREATE TABLE IF NOT EXISTS changes (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    title TEXT NOT NULL,
    type TEXT NOT NULL,
    branch TEXT NOT NULL,
    branch_created BOOLEAN NOT NULL DEFAULT FALSE,
    base_branch TEXT NOT NULL DEFAULT 'main',
    status TEXT NOT NULL,
    batch_id TEXT NOT NULL DEFAULT '',
    conflict_with TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
-- 同 (tenant, repo) 分支唯一（与 memory 实现查重语义一致）
CREATE UNIQUE INDEX IF NOT EXISTS idx_changes_tenant_repo_branch ON changes(tenant_id, repo_id, branch);

CREATE TABLE IF NOT EXISTS integration_batches (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    title TEXT NOT NULL,
    branch TEXT NOT NULL,
    status TEXT NOT NULL,
    change_ids JSONB NOT NULL DEFAULT '[]',
    pipeline_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    release_ids JSONB NOT NULL DEFAULT '[]',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);
-- 同租户集成分支唯一
CREATE UNIQUE INDEX IF NOT EXISTS idx_batches_tenant_branch ON integration_batches(tenant_id, branch);
