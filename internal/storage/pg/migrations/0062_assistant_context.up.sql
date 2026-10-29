-- 0062: 助手诊断上下文（spec 2026-10-29）。
-- assistant_sessions.context：DiagnosticContext JSONB（来源锚点/观察窗/解析状态）。
--   NULL=存量旧会话（target 已固定，投影层按 resolved 处理）；draft 会话 target 为
--   零值 JSON、context.resolutionStatus=need_choice|need_clarification。
-- 候选不落库：服务端内存缓存（TTL 一次性），伪造 fail closed。

ALTER TABLE assistant_sessions ADD COLUMN IF NOT EXISTS context JSONB;
