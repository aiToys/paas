-- 0057: 财务深化（D5-4）—— 月度预算 / 周期 token 用量 / 供应商结算对账
--
-- 与既有 billing_usages.counts 的关系：那是**当前快照**（无周期维度，token 只增不减），
-- 无法支撑「本月花了多少 / 是否超预算」。本表按 (租户, 周期, 应用, 模型) 聚合，
-- meter 每次推理回写，周期 = 记账时刻 UTC 的 YYYY-MM（容器时区不保证，不能用本地时区）。
--
-- 费用一律是估算（平台未拆分 prompt/completion），页面必须并列展示 CostBasis 口径说明。

-- 月度预算：scope=tenant 时 app_id 为空；scope=app 时必填 app_id。
-- 唯一索引保证「同租户同范围只有一条」——重复预算会让 enforce 语义不可判定。
CREATE TABLE IF NOT EXISTS billing_budgets (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    scope          TEXT NOT NULL,                 -- tenant | app
    app_id         TEXT NOT NULL DEFAULT '',
    monthly_amount DOUBLE PRECISION NOT NULL,     -- 每月上限（元）
    warn_ratio     DOUBLE PRECISION NOT NULL DEFAULT 0.8,
    enforce        BOOLEAN NOT NULL DEFAULT FALSE,
    note           TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_budgets_scope
    ON billing_budgets(tenant_id, scope, app_id);
CREATE INDEX IF NOT EXISTS idx_billing_budgets_tenant ON billing_budgets(tenant_id);

-- 周期 token 用量：主键即聚合维度，meter 走 ON CONFLICT 原子累加（并发推理不漏计）。
-- 不用 BIGSERIAL：写入是 upsert 累加，没有「事件行」概念，多一行 id 只会白白膨胀。
CREATE TABLE IF NOT EXISTS token_usage (
    tenant_id  TEXT NOT NULL,
    period     TEXT NOT NULL,                     -- YYYY-MM（UTC）
    app_id     TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL,
    tokens     BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, period, app_id, model)
);
-- 管理端按周期跨租户查 / 供应商对账：period 是唯一非租户前缀的高选择性条件。
CREATE INDEX IF NOT EXISTS idx_token_usage_period ON token_usage(period);

-- 供应商结算对账：平台没有供应商账单 API，本表是**人工登记**的供应商侧金额，
-- 与平台估算并列展示差异（diff）；不代表已付款、也不代表供应商认可。
CREATE TABLE IF NOT EXISTS vendor_settlements (
    id          TEXT PRIMARY KEY,
    vendor      TEXT NOT NULL,
    period      TEXT NOT NULL,                    -- YYYY-MM
    amount      DOUBLE PRECISION NOT NULL,        -- 供应商账单金额（元）
    currency    TEXT NOT NULL DEFAULT 'CNY',
    invoice_no  TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    recorded_by TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vendor_settlements_period ON vendor_settlements(period);

ALTER TABLE billing_budgets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS billing_budgets_tenant_isolation ON billing_budgets;
CREATE POLICY billing_budgets_tenant_isolation ON billing_budgets
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE token_usage ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS token_usage_tenant_isolation ON token_usage;
CREATE POLICY token_usage_tenant_isolation ON token_usage
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

-- vendor_settlements 是平台级人工对账数据（无 tenant_id 列，按性质不启用 RLS）：
-- 供应商账单不按租户拆分，行级隔离会把同一供应商的多租户用量切碎，失去对账意义。
