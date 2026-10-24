CREATE TABLE error_reports (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    fingerprint NVARCHAR(128) NOT NULL,
    source NVARCHAR(16) NOT NULL,
    severity NVARCHAR(16) NOT NULL,
    code NVARCHAR(128) NOT NULL DEFAULT '',
    message NVARCHAR(MAX) NOT NULL,
    stack NVARCHAR(MAX) NOT NULL DEFAULT '',
    details NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    request_id NVARCHAR(128) NOT NULL DEFAULT '',
    username NVARCHAR(255) NOT NULL DEFAULT '',
    count INT NOT NULL DEFAULT 1,
    first_seen DATETIME2(6) NOT NULL,
    last_seen DATETIME2(6) NOT NULL,
    resolved BIT NOT NULL DEFAULT 0,
    resolved_at DATETIME2(6) NULL,
    resolved_by NVARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT error_reports_fingerprint UNIQUE (fingerprint),
    CONSTRAINT error_reports_source_check CHECK (source IN ('api','pipeline','worker','storage','client','supervisor','startup','llm')),
    CONSTRAINT error_reports_severity_check CHECK (severity IN ('error','panic','fatal')),
    CONSTRAINT error_reports_count_check CHECK (count >= 1),
    CONSTRAINT error_reports_details_check CHECK (ISJSON(details) = 1)
)
--;;
CREATE INDEX error_reports_state_idx ON error_reports (resolved, last_seen DESC)
--;;
CREATE INDEX error_reports_source_idx ON error_reports (source, last_seen DESC)
--;;
CREATE INDEX error_reports_last_idx ON error_reports (last_seen)
--;;
CREATE TABLE error_occurrences (
    id BIGINT IDENTITY(1,1) NOT NULL PRIMARY KEY,
    report_id NVARCHAR(36) NOT NULL,
    [at] DATETIME2(6) NOT NULL,
    request_id NVARCHAR(128) NOT NULL DEFAULT '',
    username NVARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT error_occurrences_report_fk FOREIGN KEY (report_id) REFERENCES error_reports(id) ON DELETE CASCADE
)
--;;
CREATE INDEX error_occurrences_report_idx ON error_occurrences (report_id, [at] DESC)
