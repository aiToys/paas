-- workload 持久化：应用运行形态（Service/Job/CronJob）。
-- 多租户：tenant_id 强制过滤（查询层显式 WHERE，无 RG 继续）。
-- lane_id 默认 'default'（基线单例）；replicas/ready INTEGER；
-- idx_wl_lookup 加速 List(tenant_id, env_id, app_id, type) 多过滤组合。
CREATE TABLE IF NOT EXISTS workloads (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    env_id     TEXT NOT NULL DEFAULT '',
    lane_id    TEXT NOT NULL DEFAULT 'default',
    type       TEXT NOT NULL,
    name       TEXT NOT NULL,
    image      TEXT NOT NULL DEFAULT '',
    image_ref  TEXT NOT NULL DEFAULT '',
    replicas   INTEGER NOT NULL DEFAULT 0,
    ready      INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT '',
    schedule   TEXT NOT NULL DEFAULT '',
    command    TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wl_lookup ON workloads(tenant_id, env_id, app_id, type);
