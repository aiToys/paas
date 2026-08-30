-- configcenter 共享配置引用（shared ns → 应用派生 ns）：
-- 应用维度发现三层 merge 的引用关系真源（shared 引用 → app×env 基线 → lane 覆盖）。
CREATE TABLE IF NOT EXISTS cc_ns_refs (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    app_ns_id    TEXT NOT NULL,   -- 应用派生 ns（引用方；各 env 独立引用，隔离天然生效）
    shared_ns_id TEXT NOT NULL,   -- shared ns（被引用方）
    created_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, app_ns_id, shared_ns_id)
);
-- 影响面反查（shared 发布时展示被哪些应用引用）。
CREATE INDEX IF NOT EXISTS idx_cc_ns_refs_shared ON cc_ns_refs (tenant_id, shared_ns_id);
