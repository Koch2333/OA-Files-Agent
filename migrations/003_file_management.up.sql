BEGIN;
-- Ledger rows can now also be 'removed' (file moved to the trash from the file manager).
ALTER TABLE ledger_entries DROP CONSTRAINT IF EXISTS ledger_entries_status_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back','removed'));
ALTER TABLE ledger_entries ADD COLUMN removed_trash_id UUID;
CREATE INDEX ledger_path_idx ON ledger_entries(archive_path);
CREATE INDEX ledger_trash_idx ON ledger_entries(removed_trash_id) WHERE removed_trash_id IS NOT NULL;
COMMIT;
