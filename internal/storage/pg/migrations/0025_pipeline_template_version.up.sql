-- 0025: pipeline_templates 加 version 列（builtin 模板升级机制）。
-- builtin 模板代码改动后，SeedTemplates 按代码 Version > DB Version 覆盖 stages/name/description，
-- 解决「改 BuiltinTemplates() 代码后已部署 PG 仍是旧记录」痛点（此前每次改要手写 migration UPDATE 补救，如 0020）。
-- 存量 builtin 模板回填 version=1（与当前 BuiltinTemplates() 代码 Version=1 对齐，启动不覆盖）。
ALTER TABLE pipeline_templates ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 0;
UPDATE pipeline_templates SET version = 1 WHERE builtin = true AND version = 0;
