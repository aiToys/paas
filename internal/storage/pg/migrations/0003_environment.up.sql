-- environment 持久化：物理环境（生产/测试），独立一等公民。
-- 多租户：tenant_id 强制过滤；UNIQUE(tenant_id, name) 保证租户内名唯一。
CREATE TABLE IF NOT EXISTS environments (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    cluster    TEXT NOT NULL DEFAULT '',
    "desc"     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_env_tenant ON environments(tenant_id);
