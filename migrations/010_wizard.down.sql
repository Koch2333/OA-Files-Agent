-- Manual recovery reference only.
BEGIN;
ALTER TABLE auth_settings DROP COLUMN IF EXISTS wizard_finished;
DELETE FROM schema_migrations WHERE version = 10;
COMMIT;
