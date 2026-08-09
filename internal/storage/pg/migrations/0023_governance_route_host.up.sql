-- 0023: governance Route 加 host 列（对外域名配置）。
-- Route.Host 非空表示该路由按 Host 头匹配（多租户/多应用共用 ingress 按域名路由）；
-- 空表示不限 Host（向后兼容，任意 Host 头都可匹配此路由）。
ALTER TABLE gov_routes ADD COLUMN IF NOT EXISTS host TEXT NOT NULL DEFAULT '';
