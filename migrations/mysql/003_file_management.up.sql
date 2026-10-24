ALTER TABLE ledger_entries DROP CHECK ledger_entries_status_check
--;;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back','removed'))
--;;
ALTER TABLE ledger_entries ADD COLUMN removed_trash_id CHAR(36) NULL
--;;
CREATE INDEX ledger_path_idx ON ledger_entries(archive_path(255))
--;;
CREATE INDEX ledger_trash_idx ON ledger_entries(removed_trash_id)
