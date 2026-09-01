DROP TABLE IF EXISTS alert_events;
DROP TABLE IF EXISTS alert_states;
-- RLS 随表删除；alert_rules 表保留（0030 所有），仅拆策略
DROP POLICY IF EXISTS alert_rules_tenant_isolation ON alert_rules;
