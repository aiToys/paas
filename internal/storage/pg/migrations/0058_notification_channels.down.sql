ALTER TABLE alert_rules DROP COLUMN IF EXISTS channel_ids;
DROP TABLE IF EXISTS notification_channels;
