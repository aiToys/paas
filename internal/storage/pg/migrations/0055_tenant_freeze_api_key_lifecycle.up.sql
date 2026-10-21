-- 0055: 租户冻结与 API Key 生命周期（D5）
-- 冻结只阻断控制面准入（登录 / API Key 鉴权），不触碰数据面已部署业务。
-- 存量租户 status 默认 'active'，语义与旧行为一致（零迁移）。
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS frozen_at timestamptz;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS frozen_reason text NOT NULL DEFAULT '';

-- Key 生命周期：expires_at 空=永不过期；revoked_at 软吊销（保留记录供审计）；
-- last_used_at 由网关每分钟至多回写一次；rotated_from 指向轮换前的旧 Key。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS last_used_at timestamptz;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS rotated_from text NOT NULL DEFAULT '';

-- 到期巡检（管理页「即将到期」列表）只扫有到期时间的少数行。
CREATE INDEX IF NOT EXISTS idx_apikeys_expires ON api_keys(expires_at) WHERE expires_at IS NOT NULL;
