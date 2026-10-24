CREATE TABLE storage_connections (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    name NVARCHAR(255) COLLATE Latin1_General_100_BIN2 NOT NULL,
    name_lc AS LOWER(name) PERSISTED,
    kind NVARCHAR(64) NOT NULL,
    settings NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    secrets_enc VARBINARY(MAX) NULL,
    base_path NVARCHAR(MAX) NOT NULL DEFAULT '',
    enabled BIT NOT NULL DEFAULT 1,
    use_for_import BIT NOT NULL DEFAULT 1,
    replicate NVARCHAR(16) NOT NULL DEFAULT 'off',
    auto_since DATETIME2(6) NULL,
    last_test NVARCHAR(MAX) NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT storage_connections_name_check CHECK (LEN(name) BETWEEN 1 AND 80),
    CONSTRAINT storage_connections_replicate_check CHECK (replicate IN ('off','manual','auto')),
    CONSTRAINT storage_connections_json_check CHECK (ISJSON(settings) = 1 AND (last_test IS NULL OR ISJSON(last_test) = 1))
)
--;;
CREATE UNIQUE INDEX storage_connections_name_idx ON storage_connections (name_lc)
--;;
CREATE TABLE replications (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    connection_id NVARCHAR(36) NOT NULL,
    ledger_id NVARCHAR(36) NOT NULL,
    local_path NVARCHAR(MAX) NOT NULL,
    remote_path NVARCHAR(MAX) NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 NVARCHAR(64) NOT NULL,
    status NVARCHAR(16) NOT NULL DEFAULT 'queued',
    attempts INT NOT NULL DEFAULT 0,
    error NVARCHAR(MAX) NULL,
    next_attempt_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT replications_conn_ledger UNIQUE (connection_id, ledger_id),
    CONSTRAINT replications_conn_fk FOREIGN KEY (connection_id) REFERENCES storage_connections(id) ON DELETE CASCADE,
    CONSTRAINT replications_ledger_fk FOREIGN KEY (ledger_id) REFERENCES ledger_entries(id) ON DELETE CASCADE,
    CONSTRAINT replications_status_check CHECK (status IN ('queued','running','done','skipped','failed'))
)
--;;
CREATE INDEX replications_queue_idx ON replications (status, next_attempt_at)
--;;
CREATE INDEX replications_conn_idx ON replications (connection_id, status)
