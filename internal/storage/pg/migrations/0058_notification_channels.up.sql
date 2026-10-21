-- 0058: 告警通知渠道（D5-4「通知渠道与抑制策略」）
--
-- 背景：alert_rules.webhook_url 是单一地址，无法表达「多个渠道」「临时静默」「重复提醒」。
-- 本表把通知目标提升为一等实体：URL/自定义头（敏感，响应掩码）、启用开关、
-- 静默截止（MuteUntil，运维窗口）、重复提醒间隔（CooldownMinutes，0=只在状态转变通知）。
--
-- alert_rules.channel_ids：规则引用的渠道 ID 列表（同租户）。非空时投递这些渠道并忽略
-- webhook_url；为空回落 webhook_url（存量规则零迁移）。引用不存在的渠道在引擎解析时忽略，
-- 不级联删除规则——渠道删除不应悄悄改变规则的告警行为。
--
-- 抑制策略只做三段：启用开关 / 静默截止 / 重复抑制。跨规则抑制（A 告警存在时压 B）
-- 需要规则间依赖图与优先级，本期不做（记入遗留债务），不做半成品。

CREATE TABLE IF NOT EXISTS notification_channels (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL,
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL DEFAULT 'webhook',
    url              TEXT NOT NULL,
    headers          JSONB,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    cooldown_minutes INTEGER NOT NULL DEFAULT 0,   -- 0 = 只在 firing/resolved 转变时通知
    mute_until       TIMESTAMPTZ,                  -- NULL/过去 = 未静默
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL
);
-- 同租户重名拒绝：列表按名称区分渠道，重名会让「这次发到哪个」不可判定。
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_channels_name
    ON notification_channels(tenant_id, name);
CREATE INDEX IF NOT EXISTS idx_notification_channels_tenant
    ON notification_channels(tenant_id);

ALTER TABLE alert_rules ADD COLUMN IF NOT EXISTS channel_ids JSONB;

ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_channels_tenant_isolation ON notification_channels;
CREATE POLICY notification_channels_tenant_isolation ON notification_channels
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
