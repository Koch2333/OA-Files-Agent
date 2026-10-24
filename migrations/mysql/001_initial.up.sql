-- MySQL 8.0+ schema, version 1. Statements are separated by lines containing only "--;;" (MySQL DDL auto-commits,
-- so the migrator records each finished step and can resume a half-applied migration).
CREATE TABLE import_jobs (
    id CHAR(36) NOT NULL PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'queued',
    total_files INT NOT NULL DEFAULT 0,
    processed_files INT NOT NULL DEFAULT 0,
    success_count INT NOT NULL DEFAULT 0,
    failure_count INT NOT NULL DEFAULT 0,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT import_jobs_status_check CHECK (status IN ('queued','scanning','extracting','analyzing','planning','awaiting_review','approved','archiving','completed','partial_failure','failed','cancelled')),
    CONSTRAINT import_jobs_total_check CHECK (total_files >= 0),
    CONSTRAINT import_jobs_processed_check CHECK (processed_files >= 0 AND processed_files <= total_files),
    CONSTRAINT import_jobs_success_check CHECK (success_count >= 0),
    CONSTRAINT import_jobs_failure_check CHECK (failure_count >= 0),
    CONSTRAINT import_jobs_sum_check CHECK (success_count + failure_count = processed_files)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE file_records (
    id CHAR(36) NOT NULL PRIMARY KEY,
    task_id CHAR(36) NOT NULL,
    source_name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    sha256 CHAR(64) NOT NULL,
    size_bytes BIGINT NOT NULL,
    detected_department TEXT NULL,
    detected_type TEXT NULL,
    detected_date DATE NULL,
    evidence JSON NOT NULL DEFAULT (JSON_ARRAY()),
    review_required BOOLEAN NOT NULL DEFAULT TRUE,
    archive_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT file_records_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id),
    CONSTRAINT file_records_sha_check CHECK (REGEXP_LIKE(sha256, '^[a-f0-9]{64}$', 'c')),
    CONSTRAINT file_records_size_check CHECK (size_bytes >= 0),
    CONSTRAINT file_records_evidence_check CHECK (JSON_TYPE(evidence) = 'ARRAY'),
    CONSTRAINT file_records_archive_check CHECK (archive_status IN ('pending','archived','failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX file_records_task_idx ON file_records(task_id)
