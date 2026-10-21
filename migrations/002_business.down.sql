-- Manual recovery reference only. Drops all business data added by 002.
BEGIN;
DROP TABLE IF EXISTS correction_proposals, audit_logs, ledger_entries, archive_operations, archive_plan_items, archive_plans, archive_rules, oa_templates, agent_events CASCADE;
ALTER TABLE file_records DROP COLUMN IF EXISTS error, DROP COLUMN IF EXISTS stored_path, DROP COLUMN IF EXISTS ocr_used, DROP COLUMN IF EXISTS template_id, DROP COLUMN IF EXISTS analysis, DROP COLUMN IF EXISTS processed;
ALTER TABLE import_jobs DROP COLUMN IF EXISTS plan_id, DROP COLUMN IF EXISTS template_id, DROP COLUMN IF EXISTS idempotency_key, DROP COLUMN IF EXISTS request_fingerprint, DROP COLUMN IF EXISTS cancel_requested, DROP COLUMN IF EXISTS last_error, DROP COLUMN IF EXISTS event_seq;
DELETE FROM schema_migrations WHERE version = 2;
COMMIT;
