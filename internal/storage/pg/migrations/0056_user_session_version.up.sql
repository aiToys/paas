-- 0056: 会话代次（D5-3）
-- 签发 JWT 时把 users.session_version 固化进 claims.ver；改密/停用/改角色/显式撤销时 +1，
-- 已签发 token 在校验时失配即失效（access ≤15min，refresh 续期立即拒绝）。
-- 存量 token 不带 ver（omitempty），不做比对，等待自然过期——零迁移。
ALTER TABLE users ADD COLUMN IF NOT EXISTS session_version INT NOT NULL DEFAULT 0;
