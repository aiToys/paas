DROP INDEX IF EXISTS idx_apikeys_expires;
ALTER TABLE api_keys DROP COLUMN IF EXISTS rotated_from;
ALTER TABLE api_keys DROP COLUMN IF EXISTS revoked_at;
ALTER TABLE api_keys DROP COLUMN IF EXISTS last_used_at;
ALTER TABLE api_keys DROP COLUMN IF EXISTS expires_at;

ALTER TABLE tenants DROP COLUMN IF EXISTS frozen_reason;
ALTER TABLE tenants DROP COLUMN IF EXISTS frozen_at;
ALTER TABLE tenants DROP COLUMN IF EXISTS status;
