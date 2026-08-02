-- 0014: 数据服务加 connection 列（平台生成的连接信息：host/port/credentials/uri）。
-- credentials（password/token/secretKey）持久化——Create 生成一次，重启不变，K8s Secret 引用；
-- host/port/uri 是纯函数派生（name+namespace+credentials），冗余存储便于 API 直接返回。
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS connection JSONB NOT NULL DEFAULT '{}'::jsonb;
