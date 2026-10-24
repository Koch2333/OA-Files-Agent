ALTER TABLE import_jobs
    ADD COLUMN plan_id CHAR(36) NULL,
    ADD COLUMN template_id CHAR(36) NULL,
    ADD COLUMN idempotency_key VARCHAR(255) NULL,
    ADD COLUMN request_fingerprint TEXT NULL,
    ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN last_error TEXT NULL,
    ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0
--;;
CREATE UNIQUE INDEX import_jobs_idem_idx ON import_jobs(idempotency_key)
--;;
CREATE INDEX import_jobs_status_idx ON import_jobs(status, created_at)
--;;
ALTER TABLE file_records
    ADD COLUMN error TEXT NULL,
    ADD COLUMN stored_path TEXT NULL,
    ADD COLUMN ocr_used BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN template_id CHAR(36) NULL,
    ADD COLUMN analysis JSON NOT NULL DEFAULT (JSON_OBJECT()),
    ADD COLUMN processed BOOLEAN NOT NULL DEFAULT FALSE
--;;
CREATE INDEX file_records_sha_idx ON file_records(sha256)
--;;
CREATE TABLE agent_events (
    task_id CHAR(36) NOT NULL,
    seq BIGINT NOT NULL,
    ts DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    phase VARCHAR(64) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    file_id CHAR(36) NULL,
    summary TEXT NOT NULL,
    details JSON NULL,
    PRIMARY KEY (task_id, seq),
    CONSTRAINT agent_events_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE oa_templates (
    id CHAR(36) NOT NULL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INT NOT NULL DEFAULT 100,
    match_rules JSON NOT NULL DEFAULT (JSON_OBJECT()),
    field_mappings JSON NOT NULL DEFAULT (JSON_OBJECT()),
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT oa_templates_name_check CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 120)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE archive_rules (
    id CHAR(36) NOT NULL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    naming_pattern TEXT NOT NULL,
    directory_pattern TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT archive_rules_name_check CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 120)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE archive_plans (
    id CHAR(36) NOT NULL PRIMARY KEY,
    task_id CHAR(36) NOT NULL,
    status VARCHAR(32) NOT NULL,
    version INT NOT NULL DEFAULT 1,
    requires_approval BOOLEAN NOT NULL DEFAULT TRUE,
    approved_by VARCHAR(255) NULL,
    approved_at DATETIME(6) NULL,
    executed_at DATETIME(6) NULL,
    rule_id CHAR(36) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT archive_plans_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id) ON DELETE CASCADE,
    CONSTRAINT archive_plans_status_check CHECK (status IN ('draft','awaiting_review','approved','executing','executed','failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX archive_plans_task_idx ON archive_plans(task_id)
--;;
CREATE TABLE archive_plan_items (
    plan_id CHAR(36) NOT NULL,
    file_id CHAR(36) NOT NULL,
    position INT NOT NULL,
    original_name TEXT NOT NULL,
    suggested_name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    destination_path TEXT NOT NULL,
    department TEXT NULL,
    document_type TEXT NULL,
    document_date DATE NULL,
    warnings JSON NOT NULL DEFAULT (JSON_ARRAY()),
    review_required BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (plan_id, file_id),
    CONSTRAINT archive_plan_items_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id) ON DELETE CASCADE,
    CONSTRAINT archive_plan_items_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE archive_operations (
    id CHAR(36) NOT NULL PRIMARY KEY,
    plan_id CHAR(36) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    status VARCHAR(16) NOT NULL,
    results JSON NOT NULL DEFAULT (JSON_ARRAY()),
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    UNIQUE KEY archive_operations_plan_key (plan_id, idempotency_key),
    CONSTRAINT archive_operations_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id),
    CONSTRAINT archive_operations_status_check CHECK (status IN ('running','completed','failed','rolled_back'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE ledger_entries (
    id CHAR(36) NOT NULL PRIMARY KEY,
    file_id CHAR(36) NOT NULL,
    task_id CHAR(36) NOT NULL,
    plan_id CHAR(36) NOT NULL,
    operation_id CHAR(36) NULL,
    original_name TEXT NOT NULL,
    canonical_name TEXT NOT NULL,
    department VARCHAR(255) NOT NULL DEFAULT '',
    document_type VARCHAR(255) NOT NULL DEFAULT '',
    document_date VARCHAR(32) NOT NULL DEFAULT '',
    archive_path TEXT COLLATE utf8mb4_bin NOT NULL,
    sha256 CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'archived',
    executed_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    UNIQUE KEY ledger_entries_plan_file (plan_id, file_id),
    CONSTRAINT ledger_entries_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id),
    CONSTRAINT ledger_entries_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id),
    CONSTRAINT ledger_entries_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id),
    CONSTRAINT ledger_entries_op_fk FOREIGN KEY (operation_id) REFERENCES archive_operations(id),
    CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX ledger_executed_idx ON ledger_entries(executed_at DESC)
--;;
CREATE TABLE audit_logs (
    id CHAR(36) NOT NULL PRIMARY KEY,
    ts DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    actor VARCHAR(255) NOT NULL,
    action VARCHAR(255) NOT NULL,
    target TEXT NOT NULL,
    summary TEXT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX audit_ts_idx ON audit_logs(ts DESC)
--;;
CREATE TABLE correction_proposals (
    id CHAR(36) NOT NULL PRIMARY KEY,
    file_id CHAR(36) NOT NULL,
    instruction TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'proposed',
    changes JSON NOT NULL,
    impact JSON NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT correction_proposals_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id),
    CONSTRAINT correction_proposals_status_check CHECK (status IN ('proposed','approved','rejected'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
INSERT INTO archive_rules(id, name, naming_pattern, directory_pattern, enabled)
VALUES ('00000000-0000-4000-8000-000000000001', '默认命名与目录', '{date}_{department}_{type}_{original}', '{department}/{year}/{type}', TRUE)
