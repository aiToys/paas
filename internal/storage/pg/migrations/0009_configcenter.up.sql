-- configcenter 持久化：服务治理「配置中心」三件套（namespace + 配置项 draft + 发布版本快照）。
-- 多租户：tenant_id 强制过滤（查询层显式 WHERE，无 RG）；
--   - namespace: UNIQUE(tenant_id, name) 租户内名唯一。
--   - item:      UNIQUE(namespace_id, key) namespace 内 key 唯一（draft 层 upsert 语义）。
--   - publish:   UNIQUE(namespace_id, version) namespace 内版本号唯一。
-- publish.snapshot 用 JSONB 存 map[string]string（不可变，只随新 Publish 生成；nil 安全由仓储层保证）。
-- publish.status: active | rolled-back；事务级联在 DeleteNamespace 清 items + publishes（无外键）。
-- CreatePublish 在事务内：MAX(version)+1 → 快照全部 item → 插新 active → 旧 active 翻 rolled-back（version 单调 + active 唯一）。

CREATE TABLE IF NOT EXISTS cc_namespaces (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS cc_items (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    value        TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT 'text', -- text | json | yaml
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, key)
);
CREATE INDEX IF NOT EXISTS idx_ccitems_ns ON cc_items(namespace_id);

CREATE TABLE IF NOT EXISTS cc_publishes (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    version      INTEGER NOT NULL,
    snapshot     JSONB NOT NULL DEFAULT '{}'::jsonb, -- map[string]string 不可变快照
    status       TEXT NOT NULL,                      -- active | rolled-back
    created_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, version)
);
CREATE INDEX IF NOT EXISTS idx_ccpub_ns ON cc_publishes(namespace_id);
