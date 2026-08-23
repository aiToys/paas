-- 告警规则迁 PG（可观测 10 轮审查 R4-C1：memory-only 重启丢规则）。
CREATE TABLE IF NOT EXISTS alert_rules (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    name        TEXT NOT NULL,
    metric_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL DEFAULT '',
    operator    TEXT NOT NULL,
    threshold   DOUBLE PRECISION NOT NULL,
    severity    TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    webhook_url TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_rules_tenant ON alert_rules(tenant_id);
