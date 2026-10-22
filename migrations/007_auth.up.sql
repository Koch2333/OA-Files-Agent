BEGIN;
-- Enterprise login (Passkey + TOTP), roles, setup wizard. See docs/auth-design.md.
CREATE TABLE auth_settings (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    setup_completed BOOLEAN NOT NULL DEFAULT false,
    setup_token_hash TEXT,
    mode TEXT NOT NULL DEFAULT 'enterprise' CHECK (mode IN ('enterprise','local')),
    rp_id TEXT NOT NULL DEFAULT '',
    rp_name TEXT NOT NULL DEFAULT 'OA Agent',
    origins TEXT[] NOT NULL DEFAULT '{}',
    mfa_policy TEXT NOT NULL DEFAULT 'admins' CHECK (mfa_policy IN ('off','admins','all')),
    allow_password_login BOOLEAN NOT NULL DEFAULT true,
    session_idle_minutes INTEGER NOT NULL DEFAULT 60 CHECK (session_idle_minutes BETWEEN 5 AND 1440),
    session_max_hours INTEGER NOT NULL DEFAULT 12 CHECK (session_max_hours BETWEEN 1 AND 720),
    password_min_length INTEGER NOT NULL DEFAULT 12 CHECK (password_min_length BETWEEN 8 AND 128),
    lockout_threshold INTEGER NOT NULL DEFAULT 5 CHECK (lockout_threshold BETWEEN 3 AND 20),
    lockout_minutes INTEGER NOT NULL DEFAULT 15 CHECK (lockout_minutes BETWEEN 1 AND 1440),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO auth_settings (id) VALUES (1);

CREATE TABLE users (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL CHECK (length(username) BETWEEN 3 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 64),
    role TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    password_hash TEXT,
    must_change_password BOOLEAN NOT NULL DEFAULT false,
    totp_secret_enc BYTEA,
    totp_confirmed_at TIMESTAMPTZ,
    totp_last_step BIGINT NOT NULL DEFAULT 0,
    webauthn_handle BYTEA NOT NULL,
    failed_logins INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_idx ON users (lower(username));
CREATE UNIQUE INDEX users_webauthn_handle_idx ON users (webauthn_handle);

CREATE TABLE webauthn_credentials (
    id BYTEA PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 64),
    public_key BYTEA NOT NULL,
    sign_count BIGINT NOT NULL DEFAULT 0,
    clone_warning BOOLEAN NOT NULL DEFAULT false,
    aaguid BYTEA NOT NULL DEFAULT ''::bytea,
    transports TEXT[] NOT NULL DEFAULT '{}',
    backup_eligible BOOLEAN NOT NULL DEFAULT false,
    backup_state BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);

CREATE TABLE recovery_codes (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    used_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, code_hash)
);

-- id = hex(sha256(cookie value)); the cookie value itself is never stored. The CSRF token is derived from the cookie
-- value with HMAC, so no csrf_hash column is needed.
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    fresh_until TIMESTAMPTZ NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    mfa TEXT NOT NULL DEFAULT 'none' CHECK (mfa IN ('none','totp','passkey','recovery')),
    scope TEXT NOT NULL DEFAULT 'full' CHECK (scope IN ('full','enroll_mfa'))
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- One-time, short-lived challenges (mfa tokens, WebAuthn session data, pending TOTP secrets). id = hex(sha256(token)).
CREATE TABLE auth_challenges (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('login_mfa','webauthn_register','webauthn_login','webauthn_mfa','webauthn_reauth','webauthn_invite','totp_enroll')),
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX auth_challenges_expires_idx ON auth_challenges (expires_at);

CREATE TABLE invitations (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX invitations_user_idx ON invitations (user_id);
COMMIT;
