-- 消息从持久化轮次产生；新增助手正文与执行方式，旧报告原地恢复为消息。
ALTER TABLE assistant_runs ADD COLUMN IF NOT EXISTS reply TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_runs ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_assistant_session_active
 ON assistant_runs(session_id) WHERE status IN ('queued','running');
-- 保留 0060 已启用的 RLS 和租户 policy，不在对话升级中改变数据库安全边界。
-- 应用层继续强制 tenant/user 校验；跨租户 Worker 复用现有受信任 Core 连接。
