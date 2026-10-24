CREATE TABLE llm_settings (
    id INT NOT NULL PRIMARY KEY,
    mode NVARCHAR(16) NOT NULL,
    preset_id NVARCHAR(255) NOT NULL DEFAULT '',
    base_url NVARCHAR(MAX) NOT NULL DEFAULT '',
    model NVARCHAR(255) NOT NULL DEFAULT '',
    api_key_enc VARBINARY(MAX) NULL,
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT llm_settings_id_check CHECK (id = 1),
    CONSTRAINT llm_settings_mode_check CHECK (mode IN ('heuristic','llm'))
)
