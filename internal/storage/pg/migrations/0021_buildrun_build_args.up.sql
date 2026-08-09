-- build_runs 加 build_args JSONB 列（docker build --build-arg K=V 透传，如 SERVICE=product）。
-- 承载多服务应用构建参数（paas-shop dogfooding：SERVICE=product/recommend/chatbot/bff）。
-- DEFAULT '{}'（nil 安全，空 map 与无参数一致）；NOT NULL 防遗漏。
ALTER TABLE build_runs ADD COLUMN IF NOT EXISTS build_args JSONB NOT NULL DEFAULT '{}';
