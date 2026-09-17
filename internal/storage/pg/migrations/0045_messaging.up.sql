-- 0045 messaging 迁 PG（原 PG 分支用 memory 实现，重启丢 Topic/消费组元数据，R19 审计 M2）。
-- Topic 归属 MQ 数据服务资源、消费组归属 Topic；租户隔离 + RLS（0043 同款写法）。
-- 唯一性限租户内（同租户同 MQ 同名 topic / 同租户同 topic 同名消费组），跨租户同名合法
--（R19 审计 M1：跨租户误报「已存在」泄漏存在性）。
CREATE TABLE IF NOT EXISTS topics (
  id          TEXT NOT NULL,
  tenant_id   TEXT NOT NULL,
  mq_id       TEXT NOT NULL,
  name        TEXT NOT NULL,
  partitions  INTEGER NOT NULL DEFAULT 1,
  retention   TEXT NOT NULL DEFAULT '7d',
  status      TEXT NOT NULL DEFAULT 'active',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- 复合主键（tenant_id, id）：handler 生成确定性 ID "tp-<name>"，跨租户同名即同 ID，
  -- 全局主键会撞 23505；与 memory 版复合键 key(tenantID, id) 语义对齐。
  CONSTRAINT topics_pkey PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_topics_tenant_mq ON topics (tenant_id, mq_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_topics_tenant_mq_name ON topics (tenant_id, mq_id, name);

CREATE TABLE IF NOT EXISTS consumer_groups (
  id         TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  topic_id   TEXT NOT NULL,
  name       TEXT NOT NULL,
  mode       TEXT NOT NULL DEFAULT 'clustering',
  members    INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT consumer_groups_pkey PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_groups_tenant_topic ON consumer_groups (tenant_id, topic_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_groups_tenant_topic_name ON consumer_groups (tenant_id, topic_id, name);

ALTER TABLE topics ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS topics_tenant_isolation ON topics;
CREATE POLICY topics_tenant_isolation ON topics
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE consumer_groups ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumer_groups_tenant_isolation ON consumer_groups;
CREATE POLICY consumer_groups_tenant_isolation ON consumer_groups
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
