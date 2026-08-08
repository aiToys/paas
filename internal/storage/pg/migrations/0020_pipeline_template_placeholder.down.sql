-- 回滚 builtin 模板 stages（无法精确还原旧值，仅占位；实际不回滚 builtin 模板）。
-- 旧 seed envId 为空，回滚无意义；此 down 仅作 migration 框架占位。
SELECT 1;
