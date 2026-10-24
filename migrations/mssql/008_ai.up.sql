CREATE TABLE llm_personal (
    user_id NVARCHAR(255) NOT NULL PRIMARY KEY,
    mode NVARCHAR(16) NOT NULL,
    preset_id NVARCHAR(255) NOT NULL DEFAULT '',
    base_url NVARCHAR(MAX) NOT NULL DEFAULT '',
    model NVARCHAR(255) NOT NULL DEFAULT '',
    api_key_enc VARBINARY(MAX) NULL,
    vision_model NVARCHAR(255) NOT NULL DEFAULT '',
    allow_vision BIT NOT NULL DEFAULT 0,
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT llm_personal_mode_check CHECK (mode IN ('heuristic','llm'))
)
--;;
CREATE TABLE ai_policy (
    id SMALLINT NOT NULL PRIMARY KEY DEFAULT 1,
    allow_personal_llm BIT NOT NULL DEFAULT 0,
    allow_personal_private_hosts BIT NOT NULL DEFAULT 0,
    allow_auto_archive BIT NOT NULL DEFAULT 1,
    updated_at DATETIME2(6) NULL,
    CONSTRAINT ai_policy_id_check CHECK (id = 1)
)
--;;
INSERT INTO ai_policy (id) VALUES (1)
--;;
ALTER TABLE import_jobs ADD
    instruction NVARCHAR(MAX) NOT NULL DEFAULT '',
    automation NVARCHAR(16) NOT NULL DEFAULT 'review',
    llm_source NVARCHAR(16) NOT NULL DEFAULT 'none',
    llm_owner NVARCHAR(255) NOT NULL DEFAULT '',
    llm_model NVARCHAR(255) NOT NULL DEFAULT '',
    auto_ack_version NVARCHAR(64) NOT NULL DEFAULT '',
    auto_ack_at DATETIME2(6) NULL,
    auto_ack_by NVARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT import_jobs_automation_check CHECK (automation IN ('review','auto')),
    CONSTRAINT import_jobs_llm_source_check CHECK (llm_source IN ('none','global','personal'))
--;;
UPDATE import_jobs SET llm_source = 'global' WHERE status IN ('queued','scanning','extracting','analyzing','planning')
--;;
ALTER TABLE archive_operations ADD
    decision NVARCHAR(16) NOT NULL DEFAULT 'manual',
    decision_model NVARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT archive_operations_decision_check CHECK (decision IN ('manual','ai_auto'))
--;;
ALTER TABLE ledger_entries ADD
    decision NVARCHAR(16) NOT NULL DEFAULT 'manual',
    decision_model NVARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT ledger_entries_decision_check CHECK (decision IN ('manual','ai_auto'))
