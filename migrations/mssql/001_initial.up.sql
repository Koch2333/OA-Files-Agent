-- SQL Server 2019+ schema, version 1. Statements are separated by lines containing only "--;;"; the whole
-- migration runs in one transaction (SQL Server DDL is transactional).
CREATE TABLE import_jobs (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    status NVARCHAR(32) NOT NULL DEFAULT 'queued',
    total_files INT NOT NULL DEFAULT 0,
    processed_files INT NOT NULL DEFAULT 0,
    success_count INT NOT NULL DEFAULT 0,
    failure_count INT NOT NULL DEFAULT 0,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT import_jobs_status_check CHECK (status IN ('queued','scanning','extracting','analyzing','planning','awaiting_review','approved','archiving','completed','partial_failure','failed','cancelled')),
    CONSTRAINT import_jobs_total_check CHECK (total_files >= 0),
    CONSTRAINT import_jobs_processed_check CHECK (processed_files >= 0 AND processed_files <= total_files),
    CONSTRAINT import_jobs_success_check CHECK (success_count >= 0),
    CONSTRAINT import_jobs_failure_check CHECK (failure_count >= 0),
    CONSTRAINT import_jobs_sum_check CHECK (success_count + failure_count = processed_files)
)
--;;
CREATE TABLE file_records (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    task_id NVARCHAR(36) NOT NULL,
    source_name NVARCHAR(MAX) NOT NULL,
    source_path NVARCHAR(MAX) NOT NULL,
    mime_type NVARCHAR(255) NOT NULL,
    sha256 NVARCHAR(64) NOT NULL,
    size_bytes BIGINT NOT NULL,
    detected_department NVARCHAR(MAX) NULL,
    detected_type NVARCHAR(MAX) NULL,
    detected_date DATE NULL,
    evidence NVARCHAR(MAX) NOT NULL DEFAULT '[]',
    review_required BIT NOT NULL DEFAULT 1,
    archive_status NVARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT file_records_task_fk FOREIGN KEY (task_id) REFERENCES import_jobs(id),
    CONSTRAINT file_records_sha_check CHECK (LEN(sha256) = 64 AND sha256 COLLATE Latin1_General_BIN NOT LIKE '%[^a-f0-9]%'),
    CONSTRAINT file_records_size_check CHECK (size_bytes >= 0),
    CONSTRAINT file_records_evidence_check CHECK (ISJSON(evidence) = 1 AND LEFT(LTRIM(evidence), 1) = '['),
    CONSTRAINT file_records_archive_check CHECK (archive_status IN ('pending','archived','failed'))
)
--;;
CREATE INDEX file_records_task_idx ON file_records(task_id)
