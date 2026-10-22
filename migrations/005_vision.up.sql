BEGIN;
-- Optional vision fallback for poor OCR: a separate model id and an explicit opt-in (page images leave the machine).
ALTER TABLE llm_settings ADD COLUMN vision_model TEXT NOT NULL DEFAULT '';
ALTER TABLE llm_settings ADD COLUMN allow_vision BOOLEAN NOT NULL DEFAULT false;
COMMIT;
