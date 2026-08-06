-- 0009 环境发布流水线阶序（promote_order）
-- 发布流水线逐级提升：同租户内 promote_order 升序成链（test=10 → staging=15 → prod=20）。
-- 0 = 不参与流水线（promote 跳过该环境）。
ALTER TABLE environments ADD COLUMN IF NOT EXISTS promote_order INT NOT NULL DEFAULT 0;

-- 存量环境回填默认阶序（按 type：test=10, prod=20）。幂等：重跑不影响已显式配置的非 0 值
-- （DEFAULT 0 仅对未设值的行；UPDATE 仅改 type 命中且 order 仍为 0 的行）。
UPDATE environments SET promote_order = 10 WHERE type = 'test' AND promote_order = 0;
UPDATE environments SET promote_order = 20 WHERE type = 'prod' AND promote_order = 0;
