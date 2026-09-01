-- 可观测持久化收尾：告警状态机迁 PG（重启不丢 pending/firing）+ 告警历史事件 + RLS 补齐。
-- alert_rules 0030 建表未跟 0032/0035/0041 的 RLS 惯例，此处统一补。

CREATE TABLE IF NOT EXISTS alert_states (
    state_key   TEXT PRIMARY KEY,      -- ruleID|targetType|targetID（与引擎内存 map 同 key）
    tenant_id   TEXT NOT NULL,
    rule_id     TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL DEFAULT '',
    alert       JSONB NOT NULL,        -- observability.Alert 全字段快照
    tick_breach INT  NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_states_tenant ON alert_states(tenant_id);

-- 告警历史事件（只增不删，同审计惯例；应用层按租户 LIMIT 裁剪防膨胀）
CREATE TABLE IF NOT EXISTS alert_events (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    rule_id     TEXT NOT NULL,
    rule_name   TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL DEFAULT '',
    metric_name TEXT NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    threshold   DOUBLE PRECISION NOT NULL,
    operator    TEXT NOT NULL,
    severity    TEXT NOT NULL,
    status      TEXT NOT NULL,         -- firing | resolved（状态转变事件）
    fired_at    TIMESTAMPTZ NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_events_tenant ON alert_events(tenant_id, occurred_at DESC);

-- RLS 补齐（0041 同款幂等写法：app.tenant_id GUC + NULL 放行 owner 连接）
ALTER TABLE alert_rules ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS alert_rules_tenant_isolation ON alert_rules;
CREATE POLICY alert_rules_tenant_isolation ON alert_rules
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE alert_states ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS alert_states_tenant_isolation ON alert_states;
CREATE POLICY alert_states_tenant_isolation ON alert_states
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE alert_events ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS alert_events_tenant_isolation ON alert_events;
CREATE POLICY alert_events_tenant_isolation ON alert_events
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
