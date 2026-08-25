-- 应用级权限：应用成员（用户 × 应用 × 应用内角色）+ 受限开关。
-- restricted=true 时应用写操作需成员角色匹配（app-owner/maintainer/developer/viewer），
-- 解决「测试人员无发布权限」这类租户级 RBAC 表达不了的粒度。
CREATE TABLE IF NOT EXISTS app_members (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (app_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_app_members_tenant ON app_members(tenant_id);
CREATE INDEX IF NOT EXISTS idx_app_members_user ON app_members(user_id);

-- 受限开关：false=租户级 RBAC（现状）；true=成员角色制 enforcement。
ALTER TABLE applications ADD COLUMN IF NOT EXISTS restricted BOOLEAN NOT NULL DEFAULT FALSE;

-- app_members RLS（与其他租户表同款纵深防御）
ALTER TABLE app_members ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS app_members_tenant_isolation ON app_members;
CREATE POLICY app_members_tenant_isolation ON app_members
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
