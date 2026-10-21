BEGIN;
-- Tasks: link to plan/template, durable queue + idempotency
ALTER TABLE import_jobs
    ADD COLUMN plan_id UUID,
    ADD COLUMN template_id UUID,
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN request_fingerprint TEXT,
    ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_error TEXT,
    ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX import_jobs_idem_idx ON import_jobs(idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX import_jobs_status_idx ON import_jobs(status, created_at);

ALTER TABLE file_records
    ADD COLUMN error TEXT,
    ADD COLUMN stored_path TEXT,
    ADD COLUMN ocr_used BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN template_id UUID,
    ADD COLUMN analysis JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN processed BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX file_records_sha_idx ON file_records(sha256);

CREATE TABLE agent_events (
    task_id UUID NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    phase TEXT NOT NULL,
    event_type TEXT NOT NULL,
    file_id UUID,
    summary TEXT NOT NULL,
    details JSONB,
    PRIMARY KEY (task_id, seq)
);

CREATE TABLE oa_templates (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    enabled BOOLEAN NOT NULL DEFAULT true,
    priority INTEGER NOT NULL DEFAULT 100,
    match_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    field_mappings JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE archive_rules (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    naming_pattern TEXT NOT NULL,
    directory_pattern TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE archive_plans (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('draft','awaiting_review','approved','executing','executed','failed')),
    version INTEGER NOT NULL DEFAULT 1,
    requires_approval BOOLEAN NOT NULL DEFAULT true,
    approved_by TEXT,
    approved_at TIMESTAMPTZ,
    executed_at TIMESTAMPTZ,
    rule_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX archive_plans_task_idx ON archive_plans(task_id);

CREATE TABLE archive_plan_items (
    plan_id UUID NOT NULL REFERENCES archive_plans(id) ON DELETE CASCADE,
    file_id UUID NOT NULL REFERENCES file_records(id),
    position INTEGER NOT NULL,
    original_name TEXT NOT NULL,
    suggested_name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    destination_path TEXT NOT NULL,
    department TEXT,
    document_type TEXT,
    document_date DATE,
    warnings JSONB NOT NULL DEFAULT '[]'::jsonb,
    review_required BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (plan_id, file_id)
);

CREATE TABLE archive_operations (
    id UUID PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES archive_plans(id),
    idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','completed','failed','rolled_back')),
    results JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, idempotency_key)
);

CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES file_records(id),
    task_id UUID NOT NULL REFERENCES import_jobs(id),
    plan_id UUID NOT NULL REFERENCES archive_plans(id),
    operation_id UUID REFERENCES archive_operations(id),
    original_name TEXT NOT NULL,
    canonical_name TEXT NOT NULL,
    department TEXT NOT NULL DEFAULT '',
    document_type TEXT NOT NULL DEFAULT '',
    document_date TEXT NOT NULL DEFAULT '',
    archive_path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'archived' CHECK (status IN ('archived','rolled_back')),
    executed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, file_id)
);
CREATE INDEX ledger_executed_idx ON ledger_entries(executed_at DESC);

CREATE TABLE audit_logs (
    id UUID PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    summary TEXT NOT NULL
);
CREATE INDEX audit_ts_idx ON audit_logs(ts DESC);

CREATE TABLE correction_proposals (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES file_records(id),
    instruction TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed','approved','rejected')),
    changes JSONB NOT NULL,
    impact JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO archive_rules(id, name, naming_pattern, directory_pattern, enabled)
VALUES ('00000000-0000-4000-8000-000000000001', '默认命名与目录', '{date}_{department}_{type}_{original}', '{department}/{year}/{type}', true);
COMMIT;
