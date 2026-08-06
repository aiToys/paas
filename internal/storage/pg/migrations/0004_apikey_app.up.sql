-- 应用级 API Key：模型推理用量归因到应用。
-- app_id 非空 = 应用级 Key（归因具体应用）；空字符串 = 租户级 Key（管理员/通用）。
-- NOT NULL DEFAULT ''：旧行回填空串（与代码语义一致：NULL 视作租户级，但代码用 *string 容错旧 NULL）。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS app_id TEXT NOT NULL DEFAULT '';
UPDATE api_keys SET app_id = '' WHERE app_id IS NULL;
-- 单应用多 Key（历史/轮转），故无 unique 约束。LookupAPIKey 按 key 解析自带 app_id。
