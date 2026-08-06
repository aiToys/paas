-- billing_usages 加应用维度归因列：appID → resource → 用量（JSONB 两层 map）。
-- 主要给模型推理 token 计费按应用拆分（应用级 API Key 调 /v1 时归位）。
ALTER TABLE billing_usages ADD COLUMN IF NOT EXISTS by_app JSONB NOT NULL DEFAULT '{}';
