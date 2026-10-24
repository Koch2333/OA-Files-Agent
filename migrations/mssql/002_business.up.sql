ALTER TABLE import_jobs ADD
    plan_id NVARCHAR(36) NULL,
    template_id NVARCHAR(36) NULL,
    idempotency_key NVARCHAR(450) NULL,
    request_fingerprint NVARCHAR(MAX) NULL,
    cancel_requested BIT NOT NULL DEFAULT 0,
    last_error NVARCHAR(MAX) NULL,
    event_seq BIGINT NOT NULL DEFAULT 0
--;;
CREATE UNIQUE INDEX import_jobs_idem_idx ON import_jobs(idempotency_key) WHERE idempotency_key IS NOT NULL
--;;
CREATE INDEX import_jobs_status_idx ON import_jobs(status, created_at)
--;;
ALTER TABLE file_records ADD
    error NVARCHAR(MAX) NULL,
    stored_path NVARCHAR(MAX) NULL,
    ocr_used BIT NOT NULL DEFAULT 0,
    template_id NVARCHAR(36) NULL,
    analysis NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    processed BIT NOT NULL DEFAULT 0
--;;
CREATE INDEX file_records_sha_idx ON file_records(sha256)
--;;
CREATE TABLE agent_events (
    task_id NVARCHAR(36) NOT NULL,
    seq BIGINT NOT NULL,
    ts DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    phase NVARCHAR(64) NOT NULL,
    event_type NVARCHAR(64) NOT NULL,
    file_id NVARCHAR(36) NULL,
    summary NVARCHAR(MAX) NOT NULL,
    details NVARCHAR(MAX) NULL,
    PRIMARY KEY (task_id, seq),
    CONSTRAINT agent_events_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id) ON DELETE CASCADE,
    CONSTRAINT agent_events_details_check CHECK (details IS NULL OR ISJSON(details) = 1)
)
--;;
CREATE TABLE oa_templates (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    name NVARCHAR(255) NOT NULL,
    enabled BIT NOT NULL DEFAULT 1,
    priority INT NOT NULL DEFAULT 100,
    match_rules NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    field_mappings NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT oa_templates_name_check CHECK (LEN(name) BETWEEN 1 AND 120),
    CONSTRAINT oa_templates_rules_check CHECK (ISJSON(match_rules) = 1 AND ISJSON(field_mappings) = 1)
)
--;;
CREATE TABLE archive_rules (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    name NVARCHAR(255) NOT NULL,
    naming_pattern NVARCHAR(MAX) NOT NULL,
    directory_pattern NVARCHAR(MAX) NOT NULL,
    enabled BIT NOT NULL DEFAULT 1,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT archive_rules_name_check CHECK (LEN(name) BETWEEN 1 AND 120)
)
--;;
CREATE TABLE archive_plans (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    task_id NVARCHAR(36) NOT NULL,
    status NVARCHAR(32) NOT NULL,
    version INT NOT NULL DEFAULT 1,
    requires_approval BIT NOT NULL DEFAULT 1,
    approved_by NVARCHAR(255) NULL,
    approved_at DATETIME2(6) NULL,
    executed_at DATETIME2(6) NULL,
    rule_id NVARCHAR(36) NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT archive_plans_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id) ON DELETE CASCADE,
    CONSTRAINT archive_plans_status_check CHECK (status IN ('draft','awaiting_review','approved','executing','executed','failed'))
)
--;;
CREATE INDEX archive_plans_task_idx ON archive_plans(task_id)
--;;
CREATE TABLE archive_plan_items (
    plan_id NVARCHAR(36) NOT NULL,
    file_id NVARCHAR(36) NOT NULL,
    position INT NOT NULL,
    original_name NVARCHAR(MAX) NOT NULL,
    suggested_name NVARCHAR(MAX) NOT NULL,
    source_path NVARCHAR(MAX) NOT NULL,
    destination_path NVARCHAR(MAX) NOT NULL,
    department NVARCHAR(MAX) NULL,
    document_type NVARCHAR(MAX) NULL,
    document_date DATE NULL,
    warnings NVARCHAR(MAX) NOT NULL DEFAULT '[]',
    review_required BIT NOT NULL DEFAULT 0,
    PRIMARY KEY (plan_id, file_id),
    CONSTRAINT archive_plan_items_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id) ON DELETE CASCADE,
    CONSTRAINT archive_plan_items_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id),
    CONSTRAINT archive_plan_items_warnings_check CHECK (ISJSON(warnings) = 1)
)
--;;
CREATE TABLE archive_operations (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    plan_id NVARCHAR(36) NOT NULL,
    idempotency_key NVARCHAR(255) NOT NULL,
    status NVARCHAR(16) NOT NULL,
    results NVARCHAR(MAX) NOT NULL DEFAULT '[]',
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT archive_operations_plan_key UNIQUE (plan_id, idempotency_key),
    CONSTRAINT archive_operations_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id),
    CONSTRAINT archive_operations_status_check CHECK (status IN ('running','completed','failed','rolled_back')),
    CONSTRAINT archive_operations_results_check CHECK (ISJSON(results) = 1)
)
--;;
CREATE TABLE ledger_entries (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    file_id NVARCHAR(36) NOT NULL,
    task_id NVARCHAR(36) NOT NULL,
    plan_id NVARCHAR(36) NOT NULL,
    operation_id NVARCHAR(36) NULL,
    original_name NVARCHAR(MAX) NOT NULL,
    canonical_name NVARCHAR(MAX) NOT NULL,
    department NVARCHAR(255) NOT NULL DEFAULT '',
    document_type NVARCHAR(255) NOT NULL DEFAULT '',
    document_date NVARCHAR(32) NOT NULL DEFAULT '',
    archive_path NVARCHAR(MAX) COLLATE Latin1_General_100_BIN2 NOT NULL,
    sha256 NVARCHAR(64) NOT NULL,
    status NVARCHAR(16) NOT NULL DEFAULT 'archived',
    executed_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT ledger_entries_plan_file UNIQUE (plan_id, file_id),
    CONSTRAINT ledger_entries_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id),
    CONSTRAINT ledger_entries_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id),
    CONSTRAINT ledger_entries_plan_fk FOREIGN KEY (plan_id) REFERENCES archive_plans(id),
    CONSTRAINT ledger_entries_op_fk FOREIGN KEY (operation_id) REFERENCES archive_operations(id),
    CONSTRAINT ledger_entries_status_check CHECK (status IN ('archived','rolled_back'))
)
--;;
CREATE INDEX ledger_executed_idx ON ledger_entries(executed_at DESC)
--;;
CREATE TABLE audit_logs (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    ts DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    actor NVARCHAR(255) NOT NULL,
    action NVARCHAR(255) NOT NULL,
    target NVARCHAR(MAX) NOT NULL,
    summary NVARCHAR(MAX) NOT NULL
)
--;;
CREATE INDEX audit_ts_idx ON audit_logs(ts DESC)
--;;
CREATE TABLE correction_proposals (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    file_id NVARCHAR(36) NOT NULL,
    instruction NVARCHAR(MAX) NOT NULL,
    status NVARCHAR(16) NOT NULL DEFAULT 'proposed',
    changes NVARCHAR(MAX) NOT NULL,
    impact NVARCHAR(MAX) NOT NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT correction_proposals_file_fk FOREIGN KEY (file_id) REFERENCES file_records(id),
    CONSTRAINT correction_proposals_status_check CHECK (status IN ('proposed','approved','rejected')),
    CONSTRAINT correction_proposals_json_check CHECK (ISJSON(changes) = 1 AND ISJSON(impact) = 1)
)
--;;
INSERT INTO archive_rules(id, name, naming_pattern, directory_pattern, enabled)
VALUES ('00000000-0000-4000-8000-000000000001', N'默认命名与目录', N'{date}_{department}_{type}_{original}', N'{department}/{year}/{type}', 1)
