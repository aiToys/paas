-- 0010 发布记录增加 promoted_from（晋升来源 release ID）
-- 发布流水线逐级提升时，新 release 记录源 release ID，串成晋升链可追溯。
-- 非空 = 该 release 由 promote 产生；空 = 普通创建发布。
ALTER TABLE releases ADD COLUMN IF NOT EXISTS promoted_from TEXT NOT NULL DEFAULT '';
