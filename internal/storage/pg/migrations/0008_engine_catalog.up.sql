-- 引擎目录（平台级）：admin 配置哪些引擎可用 + 模式（managed/external-shared/external-dedicated）+
-- external-shared 共享集群连接。平台级（无 tenant_id），全租户共享。
CREATE TABLE IF NOT EXISTS engines (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    engine      TEXT NOT NULL,
    label       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    mode        TEXT NOT NULL DEFAULT 'managed',
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    connection  JSONB NOT NULL DEFAULT '{}',
    ord         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_engines_kind ON engines(kind);

-- 数据服务关联引擎（创建时从引擎目录选，kind/engine/source 由引擎决定）。
ALTER TABLE data_services ADD COLUMN IF NOT EXISTS engine_id TEXT NOT NULL DEFAULT '';
