-- 0026: workloads 加 service 列（同 app 多服务场景，如 paas-shop product/recommend/chatbot/bff）。
-- 空 = 单服务（向后兼容，CreateRelease 查找键 app×env×lane×service×type，空 service 匹配空）。
-- + 复合索引支撑 CreateRelease 按 (tenant, env, app, lane, service, type) 查找基线 Workload。
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS service TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_wl_service ON workloads(tenant_id, env_id, app_id, lane_id, service, type);
