-- billing 持久化：配额计费三件套（每租户配额 + 当前用量 + 周期账单）。
-- 多租户：tenant_id 强制过滤（查询层显式 WHERE，无 RG）；所有业务表带 tenant_id。
--   - quota:   PK(tenant_id) 每租户一份配额；limits JSONB map[string]int（-1=无限）。
--   - usage:   PK(tenant_id) 每租户一份用量；counts JSONB map[string]int。
--   - record:  UNIQUE(tenant_id, period) 同周期唯一；items JSONB []BillItem。
-- billing_quotas.limits / billing_usages.counts 用 JSONB 存 map[string]int（nil 安全由仓储层保证）。
-- billing_records.items 用 JSONB 存 []BillItem 切片（nil 安全由仓储层保证）。
--
-- CheckAndInc 横切配额拦截（原子检查+递增）：
--   事务内 SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE 串行化同租户并发，
--   超 limit 不写并回滚（与内存版 sync.Mutex 语义等价）。
-- GenerateBill 同 period unpaid 覆盖：INSERT ... ON CONFLICT (tenant_id, period) DO UPDATE。
-- PayBill 状态机 unpaid -> paid：UPDATE ... WHERE status='unpaid'，RowsAffected==0 拒绝重复支付。

CREATE TABLE IF NOT EXISTS billing_quotas (
    tenant_id  TEXT PRIMARY KEY,
    limits     JSONB NOT NULL DEFAULT '{}'::jsonb, -- map[string]int，-1=无限
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS billing_usages (
    tenant_id  TEXT PRIMARY KEY,
    counts     JSONB NOT NULL DEFAULT '{}'::jsonb, -- map[string]int
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS billing_records (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    period     TEXT NOT NULL,                          -- YYYY-MM
    items      JSONB NOT NULL DEFAULT '[]'::jsonb,     -- []BillItem
    total      DOUBLE PRECISION NOT NULL,
    status     TEXT NOT NULL,                          -- unpaid | paid
    created_at TIMESTAMPTZ NOT NULL,
    paid_at    TIMESTAMPTZ NULL,
    UNIQUE (tenant_id, period)
);
CREATE INDEX IF NOT EXISTS idx_billing_records_tenant ON billing_records(tenant_id);
