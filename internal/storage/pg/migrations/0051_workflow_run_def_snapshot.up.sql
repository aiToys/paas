-- WF-02 工作流定义快照：Start 时固化进 run，进程重启后 paused 恢复与 UI 解释旧运行以快照为准。
-- 存量 run 无快照（NULL），消费方 fallback 当前定义并标注边界。
ALTER TABLE ai_workflow_runs ADD COLUMN IF NOT EXISTS def_snapshot JSONB;
