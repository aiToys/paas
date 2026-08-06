-- 数据服务实例浅管理字段（P1）：replicas（scale 0/1 + 扩缩容）/ cpu / memory（resources 覆盖）/
-- storage_gb（PVC 容量，仅扩容）/ image（覆盖默认镜像，版本升级）。
-- 均可空（NULL/0/空串）= 用 reconciler 默认；非空覆盖。
-- source：managed（平台托管，平台拉起 Pod）| external（接入外部实例，用户填连接，不部署）。
-- 已部署 PG 增量补列（全新部署 0001 不含，IF NOT EXISTS 安全跳过）。
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS replicas INTEGER;
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS cpu TEXT NOT NULL DEFAULT '';
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS memory TEXT NOT NULL DEFAULT '';
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS storage_gb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS image TEXT NOT NULL DEFAULT '';
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'managed';

