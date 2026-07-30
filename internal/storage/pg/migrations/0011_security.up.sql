-- security 持久化：租户级 / 平台级密钥证书资产 + 审计日志（只增不删）。
-- 多租户：tenant 级 Secret 显式 WHERE tenant_id 过滤；platform 级 TenantID 为 NULL，
--   全租户可见（ListSecrets 用 OR scope='platform'，Resolve 仅 platform 返明文）。
--
-- 唯一性（两个 partial unique index 互不干扰）：
--   - uniq_secret_platform: WHERE scope='platform' 按 name 全局唯一（跨租户共享资产）
--   - uniq_secret_tenant:   WHERE scope='tenant'  按 (tenant_id, name) 租户内唯一
-- partial index 保证平台级与租户级同名不冲突（语义隔离）。
--
-- Secret 值后端明文存储（Value TEXT），List/Get/Create 返回固定掩码（不泄漏长度/内容），
-- 仅 Resolve 平台级路径返明文（供第三方供应商通道运行时解析 API Key）。
-- 真实加密存储（KMS/Vault）留后续。
-- AuditLog 只增不删（合规），按 (tenant_id, at) 倒序查询。

CREATE TABLE IF NOT EXISTS secrets (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NULL,                 -- 平台级为 NULL；tenant 级强制 NOT NULL（应用层写入）
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,             -- secret | certificate
    scope      TEXT NOT NULL,             -- tenant | platform
    value      TEXT NOT NULL,             -- 明文存储，API 返回掩码
    "desc"     TEXT NOT NULL DEFAULT '',  -- desc 是 PG 保留字，强制引用
    updated_at TIMESTAMPTZ NOT NULL
);
-- 平台级全局唯一（WHERE scope='platform'，tenant_id 为 NULL 不参与约束）。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_secret_platform ON secrets(name) WHERE scope = 'platform';
-- 租户内唯一（仅对 tenant 级行生效；平台级行被排除避免冲突）。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_secret_tenant ON secrets(tenant_id, name) WHERE scope = 'tenant';
-- 租户级 ListSecrets 过滤索引（平台级全表可见无需索引）。
CREATE INDEX IF NOT EXISTS idx_secrets_tenant ON secrets(tenant_id) WHERE scope = 'tenant';

CREATE TABLE IF NOT EXISTS audit_logs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,          -- create | update | delete
    resource_type TEXT NOT NULL,          -- secret（预留扩展）
    resource_id   TEXT NOT NULL,
    detail        TEXT NOT NULL DEFAULT '',
    at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_tenant_time ON audit_logs(tenant_id, at DESC);
