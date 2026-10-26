CREATE INDEX ledger_sha_idx ON ledger_entries(sha256) WHERE status = 'archived'
