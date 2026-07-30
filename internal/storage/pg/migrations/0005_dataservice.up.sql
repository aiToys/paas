-- dataservice 持久化：数据服务资源（DB/缓存/MQ/存储/向量/搜索，按 kind 区分）。
-- 多租户：tenant_id 强制过滤；UNIQUE(tenant_id, name) 实现租户内 name 唯一。
-- spec 用 JSONB 存 map[string]string；nil 安全由仓储层保证（读出空 map 非 nil）。
CREATE TABLE IF NOT EXISTS data_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    spec       JSONB NOT NULL DEFAULT '{}'::jsonb,
    status     TEXT NOT NULL,
    env_id     TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_ds_tenant_kind ON data_services(tenant_id, kind);
