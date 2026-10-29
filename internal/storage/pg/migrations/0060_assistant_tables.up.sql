-- 0060: 控制台 AI 助手三表（spec 2026-10-27 §10）
--
-- assistant_sessions：会话（Agent + 授权目标固定；换目标/Agent 建新会话）。
-- assistant_runs：异步任务（状态机 queued/running/completed/failed/cancelled/timed_out；
--   幂等键 + 请求 hash；lease/heartbeat/fencing 支撑多副本 Worker；窗口创建时固定 UTC）。
-- assistant_run_events：结构化进度事件（seq 单调，UNIQUE(run_id, seq)；
--   ?afterSeq= 增量轮询）。
--
-- 隔离：三表均带 tenant_id + owner_user_id，读写强制双过滤（跨租户统一 not found）。
-- 幂等：UNIQUE(tenant_id, owner_user_id, session_id, idempotency_key)——同键重放返回原任务。

CREATE TABLE IF NOT EXISTS assistant_sessions (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    owner_user_id TEXT NOT NULL DEFAULT '',
    agent_key     TEXT NOT NULL,
    agent_version TEXT NOT NULL DEFAULT '',
    target        JSONB NOT NULL,             -- diagnose.AuthorizedTarget（服务端解析快照）
    focus         JSONB,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_assistant_sessions_tenant ON assistant_sessions (tenant_id, owner_user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS assistant_runs (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES assistant_sessions(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL,
    owner_user_id   TEXT NOT NULL DEFAULT '',
    question        TEXT NOT NULL,
    symptom         TEXT NOT NULL DEFAULT '',
    parent_run_id   TEXT NOT NULL DEFAULT '',
    window_start    TIMESTAMPTZ NOT NULL,     -- 创建时固定 UTC（防执行漂移）
    window_end      TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued',
    quality         TEXT NOT NULL DEFAULT '', -- 终态填：sufficient/partial/inconclusive/invalid_output
    report          JSONB,                    -- 版本化用户侧报告（已脱敏投影）
    evidence        JSONB,                    -- 证据账本投影
    model           TEXT NOT NULL DEFAULT '',
    usage_tokens    INTEGER NOT NULL DEFAULT 0,
    usage_source    TEXT NOT NULL DEFAULT '', -- provider | estimated（口径明示）
    idempotency_key TEXT NOT NULL DEFAULT '',
    request_hash    TEXT NOT NULL DEFAULT '',
    cancel_flag     BOOLEAN NOT NULL DEFAULT FALSE,
    error_code      TEXT NOT NULL DEFAULT '',
    error_msg       TEXT NOT NULL DEFAULT '',
    lease_owner     TEXT NOT NULL DEFAULT '', -- Worker 租约（多副本 fencing）
    lease_expires   TIMESTAMPTZ,              -- NULL=非 running 或未被领取
    created_at      TIMESTAMPTZ NOT NULL,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_assistant_runs_idem
    ON assistant_runs (tenant_id, owner_user_id, session_id, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS idx_assistant_runs_session ON assistant_runs (session_id, created_at DESC);
-- Worker 队列扫描：取最早 queued（LIMIT 1 FOR UPDATE SKIP LOCKED 由 ClaimNext 用）
CREATE INDEX IF NOT EXISTS idx_assistant_runs_queue ON assistant_runs (status, created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_assistant_runs_lease ON assistant_runs (lease_expires) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS assistant_run_events (
    run_id     TEXT NOT NULL REFERENCES assistant_runs(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    type       TEXT NOT NULL,
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    tenant_id  TEXT NOT NULL DEFAULT '',   -- RLS 用冗余列（policy 引用须在建表时已存在）
    PRIMARY KEY (run_id, seq)
);

-- RLS（与既有表同款策略族；生产角色非超级用户生效）
ALTER TABLE assistant_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS assistant_sessions_tenant ON assistant_sessions;
CREATE POLICY assistant_sessions_tenant ON assistant_sessions
    USING (tenant_id = current_setting('app.tenant_id', true));
ALTER TABLE assistant_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS assistant_runs_tenant ON assistant_runs;
CREATE POLICY assistant_runs_tenant ON assistant_runs
    USING (tenant_id = current_setting('app.tenant_id', true));
ALTER TABLE assistant_run_events ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS assistant_run_events_tenant ON assistant_run_events;
CREATE POLICY assistant_run_events_tenant ON assistant_run_events
    USING (tenant_id = current_setting('app.tenant_id', true));
