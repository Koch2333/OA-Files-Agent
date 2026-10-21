BEGIN;
CREATE TABLE import_jobs (
    id UUID PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','scanning','extracting','analyzing','planning','awaiting_review','approved','archiving','completed','partial_failure','failed','cancelled')),
    total_files INTEGER NOT NULL DEFAULT 0 CHECK (total_files >= 0),
    processed_files INTEGER NOT NULL DEFAULT 0 CHECK (processed_files >= 0 AND processed_files <= total_files),
    success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (success_count + failure_count = processed_files)
);
CREATE TABLE file_records (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES import_jobs(id),
    source_name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    detected_department TEXT,
    detected_type TEXT,
    detected_date DATE,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    review_required BOOLEAN NOT NULL DEFAULT true,
    archive_status TEXT NOT NULL DEFAULT 'pending' CHECK (archive_status IN ('pending','archived','failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX file_records_task_idx ON file_records(task_id);
COMMIT;
