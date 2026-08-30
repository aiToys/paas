-- configcenter 环境隔离 + 泳道覆盖：
-- 1) cc_namespaces 加 env_id（app scope 按 (app,env) 懒建，env 空=基线，兼容存量）；
-- 2) 新表 cc_lane_overrides（泳道 key 级覆盖，无版本链，upsert 即生效）。
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS env_id TEXT NOT NULL DEFAULT '';
-- (tenant, app, env) 唯一：EnsureByAppEnv 并发幂等兜底（与 (tenant,name) 唯一约束双保险）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_cc_ns_tenant_app_env
  ON cc_namespaces(tenant_id, app_id, env_id) WHERE app_id != '';

CREATE TABLE IF NOT EXISTS cc_lane_overrides (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    env_id     TEXT NOT NULL DEFAULT '',            -- 空=全环境基线
    lane_id    TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, app_id, env_id, lane_id, key)
);
CREATE INDEX IF NOT EXISTS idx_cclo_lookup ON cc_lane_overrides(tenant_id, app_id, env_id, lane_id);

