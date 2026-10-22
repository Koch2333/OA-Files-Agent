-- Manual recovery reference only.
BEGIN;
DROP TABLE IF EXISTS error_occurrences;
DROP TABLE IF EXISTS error_reports;
DELETE FROM schema_migrations WHERE version = 9;
COMMIT;
