-- 0012: identity 密码登录支持（console-admin 身份对接）
-- 给 users 加 email / password_hash / status，name 全局唯一（登录入口）。
ALTER TABLE users ADD COLUMN IF NOT EXISTS email text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_name ON users(name);
