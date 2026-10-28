-- Manual recovery reference only (the previous default was 12; nothing is restored automatically).
BEGIN;
DELETE FROM schema_migrations WHERE version = 13;
COMMIT;
