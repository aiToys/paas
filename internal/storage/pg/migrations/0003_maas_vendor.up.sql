-- maas 供应商管理（Vendor 实体）：把 BaseURL + 凭证 + Type 抽成可复用预设，
-- 创建通道选供应商即带入，免去每个通道手填。Channel 加 vendor_id 关联。
--
-- 全新部署 0001 已含 maas_vendors 表 + maas_channels.vendor_id 列（IF NOT EXISTS 跳过，无副作用）；
-- 已部署 0001（无此列）的 PG 经本 migration 增量补齐。
ALTER TABLE maas_channels ADD COLUMN IF NOT EXISTS vendor_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS maas_vendors (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    type           TEXT NOT NULL DEFAULT 'openai-compatible',
    base_url       TEXT NOT NULL DEFAULT '',
    credential_ref TEXT NOT NULL DEFAULT '',
    "desc"         TEXT NOT NULL DEFAULT ''
);

-- 回填存量 airouter 通道的 vendor_id（按凭证引用匹配，vendor-neutral；airouter vendor 由 SeedCatalog ensure）。
UPDATE maas_channels
SET vendor_id = 'airouter'
WHERE credential_ref = 'sec-platform-airouter' AND vendor_id = '';
