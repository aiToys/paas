-- governance 持久化：服务治理三件套（注册中心 / API 网关 / 熔断器，4 表）。
-- 多租户：tenant_id 强制过滤（查询层显式 WHERE，无 RG）；UNIQUE(tenant_id, name) 实现租户内名唯一。
-- Instance.Meta 与 Route.Methods 用 JSONB 列存（读写 nil 安全由仓储层保证）。
-- CircuitBreaker.State/Stats 不持久化——由 handler 调 EvaluateBreaker 即时推导（不建列）。
-- DeleteService 级联清 instances/routes/breakers（仓储层事务保证原子，无外键）。

CREATE TABLE IF NOT EXISTS gov_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    env_id     TEXT NOT NULL,
    protocol   TEXT NOT NULL,
    port       INTEGER NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_svc_tenant_env ON gov_services(tenant_id, env_id, app_id);

CREATE TABLE IF NOT EXISTS gov_instances (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    service_id TEXT NOT NULL,
    addr       TEXT NOT NULL,
    status     TEXT NOT NULL,
    lane_id    TEXT NOT NULL DEFAULT 'default',
    meta       JSONB NOT NULL DEFAULT '{}'::jsonb, -- map[string]string
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_inst_service ON gov_instances(service_id);

CREATE TABLE IF NOT EXISTS gov_routes (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    service_id TEXT NOT NULL DEFAULT '',
    methods    JSONB NOT NULL DEFAULT '[]'::jsonb, -- []string (GET|POST|PUT|DELETE|ANY)
    strip_path BOOLEAN NOT NULL DEFAULT FALSE,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_routes_service ON gov_routes(service_id);

CREATE TABLE IF NOT EXISTS gov_breakers (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    name         TEXT NOT NULL,
    service_id   TEXT NOT NULL DEFAULT '',
    strategy     TEXT NOT NULL,
    threshold    INTEGER NOT NULL,
    min_requests INTEGER NOT NULL,
    window_secs  INTEGER NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_breakers_service ON gov_breakers(service_id);
