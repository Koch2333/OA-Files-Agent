BEGIN;
-- Single-row recognition-engine configuration. The API key is stored AES-GCM encrypted (key outside the database).
CREATE TABLE llm_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL CHECK (mode IN ('heuristic','llm')),
    preset_id TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    api_key_enc BYTEA,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMIT;
