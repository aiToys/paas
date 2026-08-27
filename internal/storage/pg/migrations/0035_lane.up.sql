-- 泳道实体化：Lane 一等实体（大项目/常驻火车/临时联调三种生命周期一套模型）。
-- 裸分支隐式泳道路径保留（EnsureByName 懒建实体），实体是增强非前置。
CREATE TABLE IF NOT EXISTS lanes (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    env_id        TEXT NOT NULL,
    name          TEXT NOT NULL,          -- DNS-1035 合法（作 K8s 资源名后缀）
    mode          TEXT NOT NULL DEFAULT 'standard',  -- standard（TTL 可回收）| permanent（常驻）
    status        TEXT NOT NULL DEFAULT 'active',    -- active | closed（资源已回收，记录保留审计）
    weight        INT  NOT NULL DEFAULT 0,           -- 入口流量权重 0-100（留位，本期恒 0）
    external_link TEXT NOT NULL DEFAULT '',          -- 外部关联（如 Jira issue key），仅展示
    description   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, env_id, name)
);
CREATE INDEX IF NOT EXISTS idx_lanes_tenant ON lanes(tenant_id);

-- lanes RLS（与其他租户表同款纵深防御）
ALTER TABLE lanes ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS lanes_tenant_isolation ON lanes;
CREATE POLICY lanes_tenant_isolation ON lanes
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
