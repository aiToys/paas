-- appconfig 持久化：工作负载级 env/Secret 键值（应用 × 环境）。
-- 多租户：tenant_id 强制过滤；UNIQUE(tenant_id, app_id, env_id, key) 实现 Upsert 语义。
-- secret 值后端明文存储，API 返回掩码（在仓储层 Masked）。
CREATE TABLE IF NOT EXISTS app_configs (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    env_id     TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    type       TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, app_id, env_id, key)
);
CREATE INDEX IF NOT EXISTS idx_appconfig_lookup ON app_configs(tenant_id, app_id, env_id);
