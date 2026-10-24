ALTER TABLE llm_settings ADD vision_model NVARCHAR(255) NOT NULL DEFAULT ''
--;;
ALTER TABLE llm_settings ADD allow_vision BIT NOT NULL DEFAULT 0
