-- 服务运行态运维操作（spec 2026-10-17）：滚动重启令牌 + 操作记录。
-- restart_token 与 Pod template annotation 贯通：token 变化 → template hash 变化 → 滚动。
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS restart_token TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS workload_operations (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL,
    workload_id      TEXT NOT NULL,
    kind             TEXT NOT NULL,             -- restart | recreate
    status           TEXT NOT NULL,             -- accepted|reconciling|running|succeeded|failed|superseded|unknown
    reason           TEXT NOT NULL DEFAULT '',
    actor            TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT NOT NULL DEFAULT '',
    target_token     TEXT NOT NULL DEFAULT '',  -- restart：目标 restartToken
    pod_uid          TEXT NOT NULL DEFAULT '',  -- recreate：被驱逐旧 Pod UID
    pod_name         TEXT NOT NULL DEFAULT '',
    replicas         INT  NOT NULL DEFAULT 0,   -- 受理时副本快照（并发扩缩容检测）
    evidence         TEXT NOT NULL DEFAULT '',  -- 观察证据摘要（追加）
    accepted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_wl_ops_workload ON workload_operations (tenant_id, workload_id, accepted_at DESC);
-- 幂等键唯一：同 (tenant, workload, key) 重复受理返回既有记录。
CREATE UNIQUE INDEX IF NOT EXISTS uq_wl_ops_idem ON workload_operations (tenant_id, workload_id, idempotency_key)
    WHERE idempotency_key <> '';
-- 同一 workload 同时只允许一个活跃维护操作（partial unique，受理层 409 的库级保证）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_wl_ops_active ON workload_operations (workload_id)
    WHERE status IN ('accepted', 'reconciling', 'running');

ALTER TABLE workload_operations ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS wl_ops_tenant_isolation ON workload_operations;
CREATE POLICY wl_ops_tenant_isolation ON workload_operations
    USING (tenant_id = current_setting('paas.tenant_id', true));
