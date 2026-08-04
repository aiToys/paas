ALTER TABLE code_repos DROP COLUMN IF EXISTS source;
ALTER TABLE code_repos DROP COLUMN IF EXISTS gitea_owner;
ALTER TABLE code_repos DROP COLUMN IF EXISTS gitea_repo;
ALTER TABLE code_repos DROP COLUMN IF EXISTS clone_url;
