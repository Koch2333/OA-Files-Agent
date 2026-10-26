-- Manual recovery reference only.
BEGIN;
ALTER TABLE archive_plan_items DROP COLUMN IF EXISTS fallback_fields;
DELETE FROM schema_migrations WHERE version = 11;
COMMIT;
