-- 配置中心发布并发加固：同 namespace 仅允许一个 active 发布（partial unique index）。
-- 先清存量脏数据：若已有多个 active，仅保留 version 最大者，其余翻 rolled-back（幂等）。
UPDATE cc_publishes p SET status = 'rolled-back'
WHERE p.status = 'active'
  AND EXISTS (
    SELECT 1 FROM cc_publishes q
    WHERE q.namespace_id = p.namespace_id
      AND q.status = 'active'
      AND (q.version > p.version OR (q.version = p.version AND q.id < p.id))
  );
CREATE UNIQUE INDEX IF NOT EXISTS uq_cc_publishes_ns_active
  ON cc_publishes(namespace_id) WHERE status = 'active';
