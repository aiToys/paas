-- 回滚 maas 供应商管理（Vendor 实体 + Channel.vendor_id）。
-- 注意：回填的 vendor_id='airouter' 不会还原（空值），但通道仍可用自身 endpoint/credential_ref。
ALTER TABLE maas_channels DROP COLUMN IF EXISTS vendor_id;
DROP TABLE IF EXISTS maas_vendors;
