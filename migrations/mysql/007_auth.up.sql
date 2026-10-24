CREATE TABLE auth_settings (
    id SMALLINT NOT NULL PRIMARY KEY DEFAULT 1,
    setup_completed BOOLEAN NOT NULL DEFAULT FALSE,
    setup_token_hash VARCHAR(255) NULL,
    mode VARCHAR(16) NOT NULL DEFAULT 'enterprise',
    rp_id VARCHAR(255) NOT NULL DEFAULT '',
    rp_name VARCHAR(255) NOT NULL DEFAULT 'OA Agent',
    origins JSON NOT NULL DEFAULT (JSON_ARRAY()),
    mfa_policy VARCHAR(16) NOT NULL DEFAULT 'admins',
    allow_password_login BOOLEAN NOT NULL DEFAULT TRUE,
    session_idle_minutes INT NOT NULL DEFAULT 60,
    session_max_hours INT NOT NULL DEFAULT 12,
    password_min_length INT NOT NULL DEFAULT 12,
    lockout_threshold INT NOT NULL DEFAULT 5,
    lockout_minutes INT NOT NULL DEFAULT 15,
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT auth_settings_id_check CHECK (id = 1),
    CONSTRAINT auth_settings_mode_check CHECK (mode IN ('enterprise','local')),
    CONSTRAINT auth_settings_mfa_check CHECK (mfa_policy IN ('off','admins','all')),
    CONSTRAINT auth_settings_idle_check CHECK (session_idle_minutes BETWEEN 5 AND 1440),
    CONSTRAINT auth_settings_max_check CHECK (session_max_hours BETWEEN 1 AND 720),
    CONSTRAINT auth_settings_pw_check CHECK (password_min_length BETWEEN 8 AND 128),
    CONSTRAINT auth_settings_lock_check CHECK (lockout_threshold BETWEEN 3 AND 20),
    CONSTRAINT auth_settings_lockmin_check CHECK (lockout_minutes BETWEEN 1 AND 1440)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
INSERT INTO auth_settings (id) VALUES (1)
--;;
CREATE TABLE users (
    id CHAR(36) NOT NULL PRIMARY KEY,
    username VARCHAR(255) COLLATE utf8mb4_bin NOT NULL,
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    role VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    password_hash TEXT NULL,
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    totp_secret_enc LONGBLOB NULL,
    totp_confirmed_at DATETIME(6) NULL,
    totp_last_step BIGINT NOT NULL DEFAULT 0,
    webauthn_handle VARBINARY(128) NOT NULL,
    failed_logins INT NOT NULL DEFAULT 0,
    locked_until DATETIME(6) NULL,
    last_login_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    updated_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT users_username_check CHECK (CHAR_LENGTH(username) BETWEEN 3 AND 64),
    CONSTRAINT users_display_check CHECK (CHAR_LENGTH(display_name) <= 64),
    CONSTRAINT users_role_check CHECK (role IN ('admin','operator','viewer')),
    CONSTRAINT users_status_check CHECK (status IN ('active','disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE UNIQUE INDEX users_username_idx ON users ((lower(username)))
--;;
CREATE UNIQUE INDEX users_webauthn_handle_idx ON users (webauthn_handle)
--;;
CREATE TABLE webauthn_credentials (
    id VARBINARY(1024) NOT NULL PRIMARY KEY,
    user_id CHAR(36) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    public_key LONGBLOB NOT NULL,
    sign_count BIGINT NOT NULL DEFAULT 0,
    clone_warning BOOLEAN NOT NULL DEFAULT FALSE,
    aaguid VARBINARY(64) NOT NULL,
    transports JSON NOT NULL DEFAULT (JSON_ARRAY()),
    backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    backup_state BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    last_used_at DATETIME(6) NULL,
    CONSTRAINT webauthn_credentials_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT webauthn_credentials_name_check CHECK (CHAR_LENGTH(name) <= 64)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id)
--;;
CREATE TABLE recovery_codes (
    user_id CHAR(36) NOT NULL,
    code_hash VARBINARY(64) NOT NULL,
    used_at DATETIME(6) NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT recovery_codes_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE TABLE sessions (
    id VARCHAR(128) NOT NULL PRIMARY KEY,
    user_id CHAR(36) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    fresh_until DATETIME(6) NOT NULL,
    ip VARCHAR(64) NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT (''),
    mfa VARCHAR(16) NOT NULL DEFAULT 'none',
    scope VARCHAR(16) NOT NULL DEFAULT 'full',
    CONSTRAINT sessions_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT sessions_mfa_check CHECK (mfa IN ('none','totp','passkey','recovery')),
    CONSTRAINT sessions_scope_check CHECK (scope IN ('full','enroll_mfa'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX sessions_user_idx ON sessions (user_id)
--;;
CREATE INDEX sessions_expires_idx ON sessions (expires_at)
--;;
CREATE TABLE auth_challenges (
    id VARCHAR(128) NOT NULL PRIMARY KEY,
    kind VARCHAR(32) NOT NULL,
    user_id CHAR(36) NULL,
    data JSON NOT NULL DEFAULT (JSON_OBJECT()),
    attempts INT NOT NULL DEFAULT 0,
    expires_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT auth_challenges_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT auth_challenges_kind_check CHECK (kind IN ('login_mfa','webauthn_register','webauthn_login','webauthn_mfa','webauthn_reauth','webauthn_invite','totp_enroll'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX auth_challenges_expires_idx ON auth_challenges (expires_at)
--;;
CREATE TABLE invitations (
    token_hash VARCHAR(128) NOT NULL PRIMARY KEY,
    user_id CHAR(36) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    used_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CONSTRAINT invitations_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
--;;
CREATE INDEX invitations_user_idx ON invitations (user_id)
