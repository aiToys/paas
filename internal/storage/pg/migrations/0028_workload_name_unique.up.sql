-- 0028: 同租户 Workload Name 唯一（审计第 6 轮 I2）。
-- Name 即 K8s Service 名（applyService 用 Workload 名），同名 Workload 会让 reconciler
-- 抢建同一 K8s Service（AlreadyOwned）触发无限 requeue。存量重名数据保留（先到先得），
-- 仅约束新写入。
CREATE UNIQUE INDEX IF NOT EXISTS workloads_tenant_name_key ON workloads(tenant_id, name);
