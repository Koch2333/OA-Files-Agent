-- Manual recovery reference only.
BEGIN;
DROP TABLE IF EXISTS replications;
DROP TABLE IF EXISTS storage_connections;
DELETE FROM schema_migrations WHERE version = 6;
COMMIT;
