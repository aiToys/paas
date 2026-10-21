-- 0059: Pipeline webhook 投递记录（D5-5「webhook 运维与有幂等保障的重放」）
--
-- 背景：webhook 是无人值守入口，跳过/失败此前只有服务端日志，用户侧黑盒；也没有补发路径。
-- 本表把每次投递落成可查记录，幂等键 = 源 delivery ID（缺失时平台生成），
-- 主键 (tenant_id, pipeline_id, id)：幂等键在单 pipeline 内唯一。
--
-- 状态：accepted（已触发 run）/ duplicate（同键重复到达，复用结果）/ skipped（有意跳过）/
-- rejected（门禁拒绝）/ failed（链路失败）。重放复用 branch/commit；已有 run 时返回该 run
-- 而非新建（幂等 no-op），重放次数落 replays。

CREATE TABLE IF NOT EXISTS pipeline_webhook_deliveries (
    tenant_id       TEXT NOT NULL,
    pipeline_id     TEXT NOT NULL,
    id              TEXT NOT NULL,             -- 幂等键：源 delivery ID 或平台生成
    app_id          TEXT NOT NULL,
    event           TEXT,
    branch          TEXT,
    commit_sha      TEXT,
    status          TEXT NOT NULL,
    reason          TEXT,
    run_id          TEXT,
    replays         INTEGER NOT NULL DEFAULT 0,
    received_at     TIMESTAMPTZ NOT NULL,
    last_replay_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, pipeline_id, id)
);
-- 列表查询：按 pipeline 最近 N 条。
CREATE INDEX IF NOT EXISTS idx_pipeline_webhook_deliveries_recent
    ON pipeline_webhook_deliveries(tenant_id, pipeline_id, received_at DESC);

ALTER TABLE pipeline_webhook_deliveries ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pipeline_webhook_deliveries_tenant_isolation ON pipeline_webhook_deliveries;
CREATE POLICY pipeline_webhook_deliveries_tenant_isolation ON pipeline_webhook_deliveries
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
