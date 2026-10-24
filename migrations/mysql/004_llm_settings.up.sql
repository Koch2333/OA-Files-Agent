CREATE TABLE llm_settings (
    id INT NOT NULL PRIMARY KEY,
    mode VARCHAR(16) NOT NULL,
    preset_id VARCHAR(255) NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT (''),
    model VARCHAR(255) NOT NULL DEFAULT '',
    api_key_enc LONGBLOB NULL,
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT llm_settings_id_check CHECK (id = 1),
    CONSTRAINT llm_settings_mode_check CHECK (mode IN ('heuristic','llm'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
