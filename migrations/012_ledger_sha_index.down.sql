-- Manual recovery reference only.
BEGIN;
DROP INDEX IF EXISTS ledger_sha_idx;
DELETE FROM schema_migrations WHERE version = 12;
COMMIT;
