CREATE TABLE llm_personal (
    user_id VARCHAR(255) NOT NULL PRIMARY KEY,
    mode VARCHAR(16) NOT NULL,
    preset_id VARCHAR(255) NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT (''),
    model VARCHAR(255) NOT NULL DEFAULT '',
    api_key_enc LONGBLOB NULL,
    vision_model VARCHAR(255) NOT NULL DEFAULT '',
    allow_vision BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT llm_personal_mode_check CHECK (mode IN ('heuristic','llm'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE ai_policy (
    id SMALLINT NOT NULL PRIMARY KEY DEFAULT 1,
    allow_personal_llm BOOLEAN NOT NULL DEFAULT FALSE,
    allow_personal_private_hosts BOOLEAN NOT NULL DEFAULT FALSE,
    allow_auto_archive BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at DATETIME(6) NULL,
    CONSTRAINT ai_policy_id_check CHECK (id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
INSERT INTO ai_policy (id) VALUES (1)
--;;
ALTER TABLE import_jobs
    ADD COLUMN instruction TEXT NOT NULL DEFAULT (''),
    ADD COLUMN automation VARCHAR(16) NOT NULL DEFAULT 'review',
    ADD COLUMN llm_source VARCHAR(16) NOT NULL DEFAULT 'none',
    ADD COLUMN llm_owner VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN llm_model VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN auto_ack_version VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN auto_ack_at DATETIME(6) NULL,
    ADD COLUMN auto_ack_by VARCHAR(255) NOT NULL DEFAULT '',
    ADD CONSTRAINT import_jobs_automation_check CHECK (automation IN ('review','auto')),
    ADD CONSTRAINT import_jobs_llm_source_check CHECK (llm_source IN ('none','global','personal'))
--;;
UPDATE import_jobs SET llm_source = 'global' WHERE status IN ('queued','scanning','extracting','analyzing','planning')
--;;
ALTER TABLE archive_operations
    ADD COLUMN decision VARCHAR(16) NOT NULL DEFAULT 'manual',
    ADD COLUMN decision_model VARCHAR(255) NOT NULL DEFAULT '',
    ADD CONSTRAINT archive_operations_decision_check CHECK (decision IN ('manual','ai_auto'))
--;;
ALTER TABLE ledger_entries
    ADD COLUMN decision VARCHAR(16) NOT NULL DEFAULT 'manual',
    ADD COLUMN decision_model VARCHAR(255) NOT NULL DEFAULT '',
    ADD CONSTRAINT ledger_entries_decision_check CHECK (decision IN ('manual','ai_auto'))
