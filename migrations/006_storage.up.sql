BEGIN;
-- Optional remote storage connections (cloud / enterprise storage). Credentials are AES-GCM encrypted (key outside the DB).
CREATE TABLE storage_connections (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    kind TEXT NOT NULL,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    secrets_enc BYTEA,
    base_path TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT true,
    use_for_import BOOLEAN NOT NULL DEFAULT true,
    replicate TEXT NOT NULL DEFAULT 'off' CHECK (replicate IN ('off','manual','auto')),
    auto_since TIMESTAMPTZ,
    last_test JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX storage_connections_name_idx ON storage_connections (lower(name));

-- One row per (connection, archived file): the copy that is pushed to the remote storage (never overwrite, never delete).
CREATE TABLE replications (
    id UUID PRIMARY KEY,
    connection_id UUID NOT NULL REFERENCES storage_connections(id) ON DELETE CASCADE,
    ledger_id UUID NOT NULL REFERENCES ledger_entries(id) ON DELETE CASCADE,
    local_path TEXT NOT NULL,
    remote_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','skipped','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, ledger_id)
);
CREATE INDEX replications_queue_idx ON replications (status, next_attempt_at);
CREATE INDEX replications_conn_idx ON replications (connection_id, status);
COMMIT;
