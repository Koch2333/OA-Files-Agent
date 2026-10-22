-- Manual recovery reference only.
BEGIN;
UPDATE ledger_entries SET status='rolled_back' WHERE status='removed';
ALTER TABLE ledger_entries DROP COLUMN IF EXISTS removed_trash_id;
ALTER TABLE ledger_entries DROP CONSTRAINT IF EXISTS ledger_entries_status_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back'));
DROP INDEX IF EXISTS ledger_path_idx;
DELETE FROM schema_migrations WHERE version = 3;
COMMIT;
