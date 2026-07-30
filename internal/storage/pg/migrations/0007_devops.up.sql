-- devops 持久化：代码 -> 构建 -> 镜像 -> 发布 主链路（4 表）。
-- 多租户：tenant_id 强制过滤（查询层显式 WHERE，无 RG）。
-- 跨模块不建外键：devops 编排调 workload.Repository 接口而非直接读写 workloads 表。
-- idx_*_app 加速按 (tenant_id, app_id) 的列表查询。
CREATE TABLE IF NOT EXISTS code_repos (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    app_id        TEXT NOT NULL,
    git_url       TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    dockerfile    TEXT NOT NULL DEFAULT '',
    build_context TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repos_app ON code_repos(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS build_runs (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    app_id      TEXT NOT NULL,
    repo_id     TEXT NOT NULL,
    trigger     TEXT NOT NULL,
    commit      TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,          -- pending|running|success|failed
    image_id    TEXT NOT NULL DEFAULT '',
    log         TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_builds_app ON build_runs(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS images (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    app_id       TEXT NOT NULL,
    registry     TEXT NOT NULL,
    tag          TEXT NOT NULL,
    digest       TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT '',
    build_run_id TEXT NOT NULL DEFAULT '',
    built_at     TIMESTAMPTZ NOT NULL,
    status       TEXT NOT NULL DEFAULT 'ready'
);
CREATE INDEX IF NOT EXISTS idx_images_app ON images(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS releases (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL,
    app_id            TEXT NOT NULL,
    env_id            TEXT NOT NULL,
    image_id          TEXT NOT NULL,
    image_digest      TEXT NOT NULL DEFAULT '',
    strategy          TEXT NOT NULL DEFAULT 'rolling',
    status            TEXT NOT NULL,
    workload_id       TEXT NOT NULL DEFAULT '',
    previous_image_id TEXT NOT NULL DEFAULT '',
    is_rollback       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL,
    created_by        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_releases_app ON releases(tenant_id, app_id);
