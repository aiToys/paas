-- application 持久化：应用 + 绑定项。
-- ResourceCount 计数不入库，读时由 Bindings Recount 派生（与内存实现一致）。
CREATE TABLE IF NOT EXISTS applications (
    id        TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    name      TEXT NOT NULL,
    initial   TEXT NOT NULL DEFAULT '',
    env       TEXT NOT NULL DEFAULT '',
    status    TEXT NOT NULL DEFAULT '',
    gradient  TEXT NOT NULL DEFAULT '',
    "desc"    TEXT NOT NULL DEFAULT '',
    replicas  TEXT NOT NULL DEFAULT '',
    rps       TEXT NOT NULL DEFAULT '',
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_apps_tenant ON applications(tenant_id);

-- 绑定项（真源）；ord 保插入顺序，列表展示稳定。
CREATE TABLE IF NOT EXISTS application_bindings (
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    ord    INTEGER NOT NULL,
    type   TEXT NOT NULL,
    name   TEXT NOT NULL,
    note   TEXT NOT NULL DEFAULT '',
    UNIQUE (app_id, type, name)
);
CREATE INDEX IF NOT EXISTS idx_bindings_app ON application_bindings(app_id);
