-- 反向迁移：按依赖逆序 DROP 4 表（releases -> images -> build_runs -> code_repos）。
DROP TABLE IF EXISTS releases CASCADE;
DROP TABLE IF EXISTS images CASCADE;
DROP TABLE IF EXISTS build_runs CASCADE;
DROP TABLE IF EXISTS code_repos CASCADE;
