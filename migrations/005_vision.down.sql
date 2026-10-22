-- Manual recovery reference only.
BEGIN;
ALTER TABLE llm_settings DROP COLUMN IF EXISTS allow_vision;
ALTER TABLE llm_settings DROP COLUMN IF EXISTS vision_model;
DELETE FROM schema_migrations WHERE version = 5;
COMMIT;
