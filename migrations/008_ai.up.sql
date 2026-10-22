BEGIN;
-- Personal LLM configuration (one row per user; the API key is AES-GCM encrypted like llm_settings.api_key_enc).
-- user_id is TEXT without a foreign key because local mode has a built-in user ("local") that is not in users.
CREATE TABLE llm_personal (
    user_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK (mode IN ('heuristic','llm')),
    preset_id TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    api_key_enc BYTEA,
    vision_model TEXT NOT NULL DEFAULT '',
    allow_vision BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Singleton administrator policy for AI features.
CREATE TABLE ai_policy (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    allow_personal_llm BOOLEAN NOT NULL DEFAULT false,
    allow_personal_private_hosts BOOLEAN NOT NULL DEFAULT false,
    allow_auto_archive BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ
);
INSERT INTO ai_policy (id) VALUES (1);

-- Per-task AI settings, fixed at creation: the pipeline reads them back from here (background workers have no HTTP user).
ALTER TABLE import_jobs
    ADD COLUMN instruction TEXT NOT NULL DEFAULT '',
    ADD COLUMN automation TEXT NOT NULL DEFAULT 'review' CHECK (automation IN ('review','auto')),
    ADD COLUMN llm_source TEXT NOT NULL DEFAULT 'none' CHECK (llm_source IN ('none','global','personal')),
    ADD COLUMN llm_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN llm_model TEXT NOT NULL DEFAULT '',
    ADD COLUMN auto_ack_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN auto_ack_at TIMESTAMPTZ,
    ADD COLUMN auto_ack_by TEXT NOT NULL DEFAULT '';
-- Tasks that were still in flight before this upgrade keep using the global engine, as before.
UPDATE import_jobs SET llm_source = 'global' WHERE status IN ('queued','scanning','extracting','analyzing','planning');

ALTER TABLE archive_operations
    ADD COLUMN decision TEXT NOT NULL DEFAULT 'manual' CHECK (decision IN ('manual','ai_auto')),
    ADD COLUMN decision_model TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger_entries
    ADD COLUMN decision TEXT NOT NULL DEFAULT 'manual' CHECK (decision IN ('manual','ai_auto')),
    ADD COLUMN decision_model TEXT NOT NULL DEFAULT '';
COMMIT;
