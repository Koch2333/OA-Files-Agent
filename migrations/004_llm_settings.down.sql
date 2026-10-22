-- Manual recovery reference only.
BEGIN;
DROP TABLE IF EXISTS llm_settings;
DELETE FROM schema_migrations WHERE version = 4;
COMMIT;
