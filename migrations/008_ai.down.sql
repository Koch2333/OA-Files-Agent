BEGIN;
ALTER TABLE ledger_entries DROP COLUMN decision_model, DROP COLUMN decision;
ALTER TABLE archive_operations DROP COLUMN decision_model, DROP COLUMN decision;
ALTER TABLE import_jobs DROP COLUMN auto_ack_by, DROP COLUMN auto_ack_at, DROP COLUMN auto_ack_version,
    DROP COLUMN llm_model, DROP COLUMN llm_owner, DROP COLUMN llm_source, DROP COLUMN automation, DROP COLUMN instruction;
DROP TABLE ai_policy;
DROP TABLE llm_personal;
COMMIT;
