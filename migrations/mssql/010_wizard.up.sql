ALTER TABLE auth_settings ADD wizard_finished BIT NOT NULL DEFAULT 0
--;;
UPDATE auth_settings SET wizard_finished = 1 WHERE setup_completed = 1
