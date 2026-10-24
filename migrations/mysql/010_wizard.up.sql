ALTER TABLE auth_settings ADD COLUMN wizard_finished BOOLEAN NOT NULL DEFAULT FALSE
--;;
UPDATE auth_settings SET wizard_finished = TRUE WHERE setup_completed = TRUE
