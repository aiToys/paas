-- 变更委派智能研发（契约 v2 §5.3）：变更记录回写 agentrun run 名，
-- 变更页状态块经 GET /api/runs/{name} 精确查询（不依赖列表过滤参数）。
ALTER TABLE changes ADD COLUMN IF NOT EXISTS agent_run_name TEXT NOT NULL DEFAULT '';
