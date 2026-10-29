DROP INDEX IF EXISTS uq_assistant_session_active;
ALTER TABLE assistant_runs DROP COLUMN IF EXISTS reply;
ALTER TABLE assistant_runs DROP COLUMN IF EXISTS mode;
