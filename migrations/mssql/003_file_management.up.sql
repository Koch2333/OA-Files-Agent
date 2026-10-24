ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_status_check
--;;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back','removed'))
--;;
ALTER TABLE ledger_entries ADD removed_trash_id NVARCHAR(36) NULL
--;;
CREATE INDEX ledger_trash_idx ON ledger_entries(removed_trash_id) WHERE removed_trash_id IS NOT NULL
