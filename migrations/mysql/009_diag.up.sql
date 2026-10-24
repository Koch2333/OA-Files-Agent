CREATE TABLE error_reports (
    id CHAR(36) NOT NULL PRIMARY KEY,
    fingerprint VARCHAR(128) NOT NULL,
    source VARCHAR(16) NOT NULL,
    severity VARCHAR(16) NOT NULL,
    code VARCHAR(128) NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    stack MEDIUMTEXT NOT NULL DEFAULT (''),
    details JSON NOT NULL DEFAULT (JSON_OBJECT()),
    request_id VARCHAR(128) NOT NULL DEFAULT '',
    username VARCHAR(255) NOT NULL DEFAULT '',
    count INT NOT NULL DEFAULT 1,
    first_seen DATETIME(6) NOT NULL,
    last_seen DATETIME(6) NOT NULL,
    resolved BOOLEAN NOT NULL DEFAULT FALSE,
    resolved_at DATETIME(6) NULL,
    resolved_by VARCHAR(255) NOT NULL DEFAULT '',
    UNIQUE KEY error_reports_fingerprint (fingerprint),
    CONSTRAINT error_reports_source_check CHECK (source IN ('api','pipeline','worker','storage','client','supervisor','startup','llm')),
    CONSTRAINT error_reports_severity_check CHECK (severity IN ('error','panic','fatal')),
    CONSTRAINT error_reports_count_check CHECK (count >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX error_reports_state_idx ON error_reports (resolved, last_seen DESC)
--;;
CREATE INDEX error_reports_source_idx ON error_reports (source, last_seen DESC)
--;;
CREATE INDEX error_reports_last_idx ON error_reports (last_seen)
--;;
CREATE TABLE error_occurrences (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    report_id CHAR(36) NOT NULL,
    at DATETIME(6) NOT NULL,
    request_id VARCHAR(128) NOT NULL DEFAULT '',
    username VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT error_occurrences_report_fk FOREIGN KEY (report_id) REFERENCES error_reports(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX error_occurrences_report_idx ON error_occurrences (report_id, at DESC)
