CREATE TABLE auth_settings (
    id SMALLINT NOT NULL PRIMARY KEY DEFAULT 1,
    setup_completed BIT NOT NULL DEFAULT 0,
    setup_token_hash NVARCHAR(255) NULL,
    mode NVARCHAR(16) NOT NULL DEFAULT 'enterprise',
    rp_id NVARCHAR(255) NOT NULL DEFAULT '',
    rp_name NVARCHAR(255) NOT NULL DEFAULT 'OA Agent',
    origins NVARCHAR(MAX) NOT NULL DEFAULT '[]',
    mfa_policy NVARCHAR(16) NOT NULL DEFAULT 'admins',
    allow_password_login BIT NOT NULL DEFAULT 1,
    session_idle_minutes INT NOT NULL DEFAULT 60,
    session_max_hours INT NOT NULL DEFAULT 12,
    password_min_length INT NOT NULL DEFAULT 12,
    lockout_threshold INT NOT NULL DEFAULT 5,
    lockout_minutes INT NOT NULL DEFAULT 15,
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT auth_settings_id_check CHECK (id = 1),
    CONSTRAINT auth_settings_mode_check CHECK (mode IN ('enterprise','local')),
    CONSTRAINT auth_settings_mfa_check CHECK (mfa_policy IN ('off','admins','all')),
    CONSTRAINT auth_settings_idle_check CHECK (session_idle_minutes BETWEEN 5 AND 1440),
    CONSTRAINT auth_settings_max_check CHECK (session_max_hours BETWEEN 1 AND 720),
    CONSTRAINT auth_settings_pw_check CHECK (password_min_length BETWEEN 8 AND 128),
    CONSTRAINT auth_settings_lock_check CHECK (lockout_threshold BETWEEN 3 AND 20),
    CONSTRAINT auth_settings_lockmin_check CHECK (lockout_minutes BETWEEN 1 AND 1440),
    CONSTRAINT auth_settings_origins_check CHECK (ISJSON(origins) = 1)
)
--;;
INSERT INTO auth_settings (id) VALUES (1)
--;;
CREATE TABLE users (
    id NVARCHAR(36) NOT NULL PRIMARY KEY,
    username NVARCHAR(255) COLLATE Latin1_General_100_BIN2 NOT NULL,
    username_lc AS LOWER(username) PERSISTED,
    display_name NVARCHAR(255) NOT NULL DEFAULT '',
    role NVARCHAR(16) NOT NULL,
    status NVARCHAR(16) NOT NULL DEFAULT 'active',
    password_hash NVARCHAR(MAX) NULL,
    must_change_password BIT NOT NULL DEFAULT 0,
    totp_secret_enc VARBINARY(MAX) NULL,
    totp_confirmed_at DATETIME2(6) NULL,
    totp_last_step BIGINT NOT NULL DEFAULT 0,
    webauthn_handle VARBINARY(128) NOT NULL,
    failed_logins INT NOT NULL DEFAULT 0,
    locked_until DATETIME2(6) NULL,
    last_login_at DATETIME2(6) NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    updated_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT users_username_check CHECK (LEN(username) BETWEEN 3 AND 64),
    CONSTRAINT users_display_check CHECK (LEN(display_name) <= 64),
    CONSTRAINT users_role_check CHECK (role IN ('admin','operator','viewer')),
    CONSTRAINT users_status_check CHECK (status IN ('active','disabled'))
)
--;;
CREATE UNIQUE INDEX users_username_idx ON users (username_lc)
--;;
CREATE UNIQUE INDEX users_webauthn_handle_idx ON users (webauthn_handle)
--;;
CREATE TABLE webauthn_credentials (
    id VARBINARY(900) NOT NULL PRIMARY KEY,
    user_id NVARCHAR(36) NOT NULL,
    name NVARCHAR(255) NOT NULL DEFAULT '',
    public_key VARBINARY(MAX) NOT NULL,
    sign_count BIGINT NOT NULL DEFAULT 0,
    clone_warning BIT NOT NULL DEFAULT 0,
    aaguid VARBINARY(64) NOT NULL,
    transports NVARCHAR(MAX) NOT NULL DEFAULT '[]',
    backup_eligible BIT NOT NULL DEFAULT 0,
    backup_state BIT NOT NULL DEFAULT 0,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    last_used_at DATETIME2(6) NULL,
    CONSTRAINT webauthn_credentials_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT webauthn_credentials_name_check CHECK (LEN(name) <= 64),
    CONSTRAINT webauthn_credentials_transports_check CHECK (ISJSON(transports) = 1)
)
--;;
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id)
--;;
CREATE TABLE recovery_codes (
    user_id NVARCHAR(36) NOT NULL,
    code_hash VARBINARY(64) NOT NULL,
    used_at DATETIME2(6) NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT recovery_codes_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)
--;;
CREATE TABLE sessions (
    id NVARCHAR(128) NOT NULL PRIMARY KEY,
    user_id NVARCHAR(36) NOT NULL,
    created_at DATETIME2(6) NOT NULL,
    last_seen_at DATETIME2(6) NOT NULL,
    expires_at DATETIME2(6) NOT NULL,
    fresh_until DATETIME2(6) NOT NULL,
    ip NVARCHAR(64) NOT NULL DEFAULT '',
    user_agent NVARCHAR(MAX) NOT NULL DEFAULT '',
    mfa NVARCHAR(16) NOT NULL DEFAULT 'none',
    scope NVARCHAR(16) NOT NULL DEFAULT 'full',
    CONSTRAINT sessions_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT sessions_mfa_check CHECK (mfa IN ('none','totp','passkey','recovery')),
    CONSTRAINT sessions_scope_check CHECK (scope IN ('full','enroll_mfa'))
)
--;;
CREATE INDEX sessions_user_idx ON sessions (user_id)
--;;
CREATE INDEX sessions_expires_idx ON sessions (expires_at)
--;;
CREATE TABLE auth_challenges (
    id NVARCHAR(128) NOT NULL PRIMARY KEY,
    kind NVARCHAR(32) NOT NULL,
    user_id NVARCHAR(36) NULL,
    data NVARCHAR(MAX) NOT NULL DEFAULT '{}',
    attempts INT NOT NULL DEFAULT 0,
    expires_at DATETIME2(6) NOT NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT auth_challenges_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT auth_challenges_kind_check CHECK (kind IN ('login_mfa','webauthn_register','webauthn_login','webauthn_mfa','webauthn_reauth','webauthn_invite','totp_enroll')),
    CONSTRAINT auth_challenges_data_check CHECK (ISJSON(data) = 1)
)
--;;
CREATE INDEX auth_challenges_expires_idx ON auth_challenges (expires_at)
--;;
CREATE TABLE invitations (
    token_hash NVARCHAR(128) NOT NULL PRIMARY KEY,
    user_id NVARCHAR(36) NOT NULL,
    expires_at DATETIME2(6) NOT NULL,
    used_at DATETIME2(6) NULL,
    created_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(),
    CONSTRAINT invitations_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)
--;;
CREATE INDEX invitations_user_idx ON invitations (user_id)
