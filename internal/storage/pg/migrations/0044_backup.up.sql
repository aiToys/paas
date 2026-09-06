-- 0044 backup 迁 PG（原进程内 mock，PG 模式重启丢记录）。
-- 备份记录表：归属数据服务资源，租户隔离（RLS 0043 已含 secrets 等同款写法——本表新增同步带）。
CREATE TABLE IF NOT EXISTS backups (
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  type        TEXT NOT NULL DEFAULT 'full',
  status      TEXT NOT NULL DEFAULT 'completed',
  size_mb     INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_backups_tenant_resource ON backups (tenant_id, resource_id);

ALTER TABLE backups ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS backups_tenant_isolation ON backups;
CREATE POLICY backups_tenant_isolation ON backups
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
