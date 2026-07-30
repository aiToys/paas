-- identity 持久化：租户 / 用户 / 角色 / API Key。
-- 多租户隔离键：所有业务表带 tenant_id，查询层显式 WHERE tenant_id=$1 过滤。
CREATE TABLE IF NOT EXISTS tenants (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);

-- 用户角色多值：一行一角色（identity.User.Roles []string）。
CREATE TABLE IF NOT EXISTS user_roles (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    TEXT NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- user_id 不加 FK：内存实现中 api_key 自带 Roles，鉴权不依赖独立 user 记录，
    -- 且 seed 仅建 tenants + api_keys（不建 users）。保持松耦合与现状一致。
    user_id    TEXT NOT NULL,
    key        TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apikeys_tenant ON api_keys(tenant_id);
CREATE INDEX IF NOT EXISTS idx_apikeys_key ON api_keys(key);

-- API Key 角色多值：鉴权按 Key 上的角色判定（identity.APIKey.Roles []string）。
CREATE TABLE IF NOT EXISTS api_key_roles (
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (api_key_id, role)
);
