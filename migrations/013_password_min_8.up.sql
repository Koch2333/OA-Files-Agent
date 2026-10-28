BEGIN;
-- The default minimum password length drops from 12 to 8 characters (still at least two character classes, no username, no common
-- passwords). Installations still on the old default move to the new one; a value an administrator chose on purpose is kept.
UPDATE auth_settings SET password_min_length = 8 WHERE password_min_length = 12;
COMMIT;
