BEGIN;
-- Content-hash lookups ("is this content already archived, and where?") must not scan the table or the disk.
CREATE INDEX IF NOT EXISTS ledger_sha_idx ON ledger_entries(sha256) WHERE status = 'archived';
COMMIT;
