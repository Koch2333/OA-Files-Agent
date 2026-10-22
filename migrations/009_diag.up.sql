BEGIN;
-- Error reports for diagnostics (internal/diag): one row per fingerprint with count and first/last seen, plus the last
-- 20 occurrences. Everything stored here is redacted first (no secrets, no document text).
CREATE TABLE error_reports (
    id UUID PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL CHECK (source IN ('api','pipeline','worker','storage','client','supervisor','startup','llm')),
    severity TEXT NOT NULL CHECK (severity IN ('error','panic','fatal')),
    code TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    stack TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    count INTEGER NOT NULL DEFAULT 1 CHECK (count >= 1),
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen TIMESTAMPTZ NOT NULL,
    resolved BOOLEAN NOT NULL DEFAULT false,
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX error_reports_state_idx ON error_reports (resolved, last_seen DESC);
CREATE INDEX error_reports_source_idx ON error_reports (source, last_seen DESC);
CREATE INDEX error_reports_last_idx ON error_reports (last_seen);

CREATE TABLE error_occurrences (
    id BIGSERIAL PRIMARY KEY,
    report_id UUID NOT NULL REFERENCES error_reports(id) ON DELETE CASCADE,
    at TIMESTAMPTZ NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT ''
);
CREATE INDEX error_occurrences_report_idx ON error_occurrences (report_id, at DESC);
COMMIT;
