-- Manual recovery reference only.
BEGIN;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS auth_challenges;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS recovery_codes;
DROP TABLE IF EXISTS webauthn_credentials;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS auth_settings;
DELETE FROM schema_migrations WHERE version = 7;
COMMIT;
