CREATE TABLE storage_connections (
    id CHAR(36) NOT NULL PRIMARY KEY,
    name VARCHAR(255) COLLATE utf8mb4_bin NOT NULL,
    kind VARCHAR(64) NOT NULL,
    settings JSON NOT NULL DEFAULT (JSON_OBJECT()),
    secrets_enc LONGBLOB NULL,
    base_path TEXT NOT NULL DEFAULT (''),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    use_for_import BOOLEAN NOT NULL DEFAULT TRUE,
    replicate VARCHAR(16) NOT NULL DEFAULT 'off',
    auto_since DATETIME(6) NULL,
    last_test JSON NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT storage_connections_name_check CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 80),
    CONSTRAINT storage_connections_replicate_check CHECK (replicate IN ('off','manual','auto'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE UNIQUE INDEX storage_connections_name_idx ON storage_connections ((lower(name)))
--;;
CREATE TABLE replications (
    id CHAR(36) NOT NULL PRIMARY KEY,
    connection_id CHAR(36) NOT NULL,
    ledger_id CHAR(36) NOT NULL,
    local_path TEXT NOT NULL,
    remote_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'queued',
    attempts INT NOT NULL DEFAULT 0,
    error TEXT NULL,
    next_attempt_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    UNIQUE KEY replications_conn_ledger (connection_id, ledger_id),
    CONSTRAINT replications_conn_fk FOREIGN KEY (connection_id) REFERENCES storage_connections(id) ON DELETE CASCADE,
    CONSTRAINT replications_ledger_fk FOREIGN KEY (ledger_id) REFERENCES ledger_entries(id) ON DELETE CASCADE,
    CONSTRAINT replications_status_check CHECK (status IN ('queued','running','done','skipped','failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX replications_queue_idx ON replications (status, next_attempt_at)
--;;
CREATE INDEX replications_conn_idx ON replications (connection_id, status)
