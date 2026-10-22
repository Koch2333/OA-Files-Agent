BEGIN;
-- The setup wizard is a server-side gate: POST /v1/setup/complete creates the administrator, but the remaining steps
-- (first MFA enrolment, recovery codes, global LLM) must finish before the application opens (POST /v1/setup/finish).
ALTER TABLE auth_settings ADD COLUMN wizard_finished BOOLEAN NOT NULL DEFAULT false;
-- Installs that predate the gate are considered finished.
UPDATE auth_settings SET wizard_finished = true WHERE setup_completed;
COMMIT;
