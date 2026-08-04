-- 0002 devops 一站式：code_repos 补 source/gitea_owner/gitea_repo/clone_url 列。
-- 背景：0001_init.up.sql 在 8/3 合入了这 4 列（DevOps 一站式内置 Gitea），但已部署的 dev PG
-- 实例 0001 是 8/1 旧版本（无此列），golang-migrate 不重跑已 applied 版本，故增量补。
-- 对全新部署无副作用：0001 已建此列，IF NOT EXISTS 跳过。
ALTER TABLE code_repos ADD COLUMN IF NOT EXISTS source       TEXT NOT NULL DEFAULT 'external';
ALTER TABLE code_repos ADD COLUMN IF NOT EXISTS gitea_owner  TEXT NOT NULL DEFAULT '';
ALTER TABLE code_repos ADD COLUMN IF NOT EXISTS gitea_repo   TEXT NOT NULL DEFAULT '';
ALTER TABLE code_repos ADD COLUMN IF NOT EXISTS clone_url    TEXT NOT NULL DEFAULT '';
