package store

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/sqldb"

	"oa-agent/backend/internal/imports"
)

var (
	ErrAlreadySetup  = errors.New("setup already completed")
	ErrBadSetupToken = errors.New("setup token invalid")
	ErrUserExists    = errors.New("username already exists")
	ErrLastAdmin     = errors.New("last active administrator")
)

type AuthSettings struct {
	SetupCompleted     bool
	WizardFinished     bool // setup wizard (MFA, recovery codes, LLM) done; the app is gated until then
	SetupTokenHash     string
	Mode               string
	RPID               string
	RPName             string
	Origins            []string
	MFAPolicy          string
	AllowPasswordLogin bool
	SessionIdleMinutes int
	SessionMaxHours    int
	PasswordMinLength  int
	LockoutThreshold   int
	LockoutMinutes     int
	UpdatedAt          time.Time
}

type AuthUser struct {
	ID                 string
	Username           string
	DisplayName        string
	Role               string
	Status             string
	PasswordHash       string // empty = passkey-only
	MustChangePassword bool
	TOTPSecretEnc      []byte
	TOTPConfirmedAt    *time.Time
	TOTPLastStep       int64
	WebAuthnHandle     []byte
	FailedLogins       int
	LockedUntil        *time.Time
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (u AuthUser) TOTPEnabled() bool { return u.TOTPConfirmedAt != nil && len(u.TOTPSecretEnc) > 0 }

// AuthUserInfo is a user with the counters the admin list needs.
type AuthUserInfo struct {
	AuthUser
	PasskeyCount           int
	RecoveryCodesRemaining int
}

type WebAuthnCred struct {
	ID             []byte
	UserID         string
	Name           string
	PublicKey      []byte
	SignCount      uint32
	CloneWarning   bool
	AAGUID         []byte
	Transports     []string
	BackupEligible bool
	BackupState    bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

type AuthSession struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	FreshUntil time.Time
	IP         string
	UserAgent  string
	MFA        string
	Scope      string
}

type AuthSessionInfo struct {
	AuthSession
	Username string
}

type AuthChallenge struct {
	ID        string
	Kind      string
	UserID    string
	Data      json.RawMessage
	Attempts  int
	ExpiresAt time.Time
}

type Invitation struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
}

const settingsCols = `setup_completed, wizard_finished, COALESCE(setup_token_hash,''), mode, rp_id, rp_name, origins, mfa_policy, allow_password_login, session_idle_minutes, session_max_hours, password_min_length, lockout_threshold, lockout_minutes, updated_at`

func scanSettings(d sqldb.Dialect, r sqldb.Row) (AuthSettings, error) {
	var a AuthSettings
	err := r.Scan(&a.SetupCompleted, &a.WizardFinished, &a.SetupTokenHash, &a.Mode, &a.RPID, &a.RPName, d.ScanStrings(&a.Origins), &a.MFAPolicy, &a.AllowPasswordLogin, &a.SessionIdleMinutes, &a.SessionMaxHours, &a.PasswordMinLength, &a.LockoutThreshold, &a.LockoutMinutes, &a.UpdatedAt)
	if a.Origins == nil {
		a.Origins = []string{}
	}
	return a, err
}

func (s *Store) AuthSettings(ctx context.Context) (AuthSettings, error) {
	return scanSettings(s.Dialect(), s.Pool.QueryRow(ctx, `SELECT `+settingsCols+` FROM auth_settings WHERE id = 1`))
}

// SaveAuthSettings writes the tunable policy fields (not the setup state).
func (s *Store) SaveAuthSettings(ctx context.Context, a AuthSettings) error {
	if a.Origins == nil {
		a.Origins = []string{}
	}
	_, err := s.Pool.Exec(ctx, `UPDATE auth_settings SET rp_id=$1, rp_name=$2, origins=$3, mfa_policy=$4, allow_password_login=$5, session_idle_minutes=$6, session_max_hours=$7, password_min_length=$8, lockout_threshold=$9, lockout_minutes=$10, updated_at=now() WHERE id=1`,
		a.RPID, a.RPName, s.Dialect().StringsArg(a.Origins), a.MFAPolicy, a.AllowPasswordLogin, a.SessionIdleMinutes, a.SessionMaxHours, a.PasswordMinLength, a.LockoutThreshold, a.LockoutMinutes)
	return err
}

// SetSetupToken stores the hash of a fresh setup token; it only works while setup is not completed.
func (s *Store) SetSetupToken(ctx context.Context, hash string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE auth_settings SET setup_token_hash=$1, updated_at=now() WHERE id=1 AND setup_completed=false`, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadySetup
	}
	return nil
}

// SetupInput is everything CompleteSetup writes in one transaction.
type SetupInput struct {
	Settings AuthSettings
	Admin    *AuthUser // nil for local mode
	// VerifyToken, when non-nil, receives the stored token hash and must accept it (constant-time compare lives with the caller).
	VerifyToken func(storedHash string) bool
}

// CompleteSetup atomically creates the administrator and flips setup_completed. The settings row is locked first, so
// concurrent attempts serialize and only one succeeds (the others get ErrAlreadySetup).
func (s *Store) CompleteSetup(ctx context.Context, in SetupInput) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	cur, err := scanSettings(tx.Dialect(), tx.QueryRow(ctx, `SELECT `+settingsCols+` FROM auth_settings WHERE id = 1 FOR UPDATE`))
	if err != nil {
		return err
	}
	if cur.SetupCompleted {
		return ErrAlreadySetup
	}
	if in.VerifyToken != nil && !in.VerifyToken(cur.SetupTokenHash) {
		return ErrBadSetupToken
	}
	if in.Admin != nil {
		if err := insertUser(ctx, tx, tx.Dialect(), *in.Admin); err != nil {
			return err
		}
	}
	a := in.Settings
	if a.Origins == nil {
		a.Origins = []string{}
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_settings SET setup_completed=true, wizard_finished=$12, setup_token_hash=NULL, mode=$1, rp_id=$2, rp_name=$3, origins=$4, mfa_policy=$5, allow_password_login=$6, session_idle_minutes=$7, session_max_hours=$8, password_min_length=$9, lockout_threshold=$10, lockout_minutes=$11, updated_at=now() WHERE id=1`,
		a.Mode, a.RPID, a.RPName, tx.Dialect().StringsArg(a.Origins), a.MFAPolicy, a.AllowPasswordLogin, a.SessionIdleMinutes, a.SessionMaxHours, a.PasswordMinLength, a.LockoutThreshold, a.LockoutMinutes, a.Mode == "local"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const userCols = `id::text, username, display_name, role, status, COALESCE(password_hash,''), must_change_password, totp_secret_enc, totp_confirmed_at, totp_last_step, webauthn_handle, failed_logins, locked_until, last_login_at, created_at, updated_at`

func scanUser(r sqldb.Row) (AuthUser, error) {
	var u AuthUser
	err := r.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PasswordHash, &u.MustChangePassword, &u.TOTPSecretEnc, &u.TOTPConfirmedAt, &u.TOTPLastStep, &u.WebAuthnHandle, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func insertUser(ctx context.Context, q sqldb.Queryer, d sqldb.Dialect, u AuthUser) error {
	_, err := q.Exec(ctx, `INSERT INTO users (id, username, display_name, role, status, password_hash, must_change_password, webauthn_handle) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.ID, u.Username, u.DisplayName, u.Role, u.Status, nullStr(u.PasswordHash), u.MustChangePassword, u.WebAuthnHandle)
	if err != nil && d.IsUniqueViolation(err) {
		return ErrUserExists
	}
	return err
}

func (s *Store) CreateAuthUser(ctx context.Context, u AuthUser) error {
	if u.ID == "" {
		id, err := imports.NewID()
		if err != nil {
			return err
		}
		u.ID = id
	}
	if u.Status == "" {
		u.Status = "active"
	}
	return insertUser(ctx, s.Pool, s.Dialect(), u)
}

func (s *Store) AuthUserByID(ctx context.Context, id string) (AuthUser, error) {
	if !validUUID(id) {
		return AuthUser{}, ErrNotFound
	}
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) AuthUserByName(ctx context.Context, username string) (AuthUser, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower($1)`, username))
}

func (s *Store) AuthUserByHandle(ctx context.Context, handle []byte) (AuthUser, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE webauthn_handle = $1`, handle))
}

type UserFilter struct {
	Q      string
	Role   string
	Status string // active | disabled | locked
	Now    time.Time
}

func (s *Store) ListAuthUsers(ctx context.Context, f UserFilter, p Page) ([]AuthUserInfo, int, error) {
	page, size := p.norm()
	where, args := []string{"1=1"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	if f.Q != "" {
		add(`(username ILIKE ? OR display_name ILIKE ?)`, likeEscape(f.Q))
	}
	if f.Role != "" {
		add(`role = ?`, f.Role)
	}
	switch f.Status {
	case "active":
		add(`(status = 'active' AND (locked_until IS NULL OR locked_until <= ?))`, f.Now)
	case "disabled":
		where = append(where, `status = 'disabled'`)
	case "locked":
		add(`(status = 'active' AND locked_until > ?)`, f.Now)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, `SELECT `+userCols+`, (SELECT count(*) FROM webauthn_credentials c WHERE c.user_id = users.id), (SELECT count(*) FROM recovery_codes r WHERE r.user_id = users.id AND r.used_at IS NULL) FROM users WHERE `+w+` ORDER BY lower(username) LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuthUserInfo{}
	for rows.Next() {
		var u AuthUserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PasswordHash, &u.MustChangePassword, &u.TOTPSecretEnc, &u.TOTPConfirmedAt, &u.TOTPLastStep, &u.WebAuthnHandle, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.PasskeyCount, &u.RecoveryCodesRemaining); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func itoa(n int) string { return strconv.Itoa(n) }

// AuthUserInfoByID returns the user with passkey/recovery counters.
func (s *Store) AuthUserInfoByID(ctx context.Context, id string) (AuthUserInfo, error) {
	u, err := s.AuthUserByID(ctx, id)
	if err != nil {
		return AuthUserInfo{}, err
	}
	out := AuthUserInfo{AuthUser: u}
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM webauthn_credentials WHERE user_id=$1), (SELECT count(*) FROM recovery_codes WHERE user_id=$1 AND used_at IS NULL)`, id).Scan(&out.PasskeyCount, &out.RecoveryCodesRemaining)
	return out, err
}

// lockAdmins serializes everything that can reduce the number of active administrators.
func lockAdmins(ctx context.Context, tx sqldb.Tx) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM auth_settings WHERE id = 1 FOR UPDATE`)
	return err
}

func activeAdminsOtherThan(ctx context.Context, tx sqldb.Tx, id string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND status='active' AND id <> $1`, id).Scan(&n)
	return n, err
}

type UserPatch struct {
	DisplayName *string
	Role        *string
	Status      *string
}

// UpdateAuthUser applies a patch; demoting or disabling the last active administrator fails with ErrLastAdmin.
func (s *Store) UpdateAuthUser(ctx context.Context, id string, p UserPatch) (AuthUser, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AuthUser{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockAdmins(ctx, tx); err != nil {
		return AuthUser{}, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return u, err
	}
	role, status, name := u.Role, u.Status, u.DisplayName
	if p.Role != nil {
		role = *p.Role
	}
	if p.Status != nil {
		status = *p.Status
	}
	if p.DisplayName != nil {
		name = *p.DisplayName
	}
	if u.Role == "admin" && u.Status == "active" && (role != "admin" || status != "active") {
		n, err := activeAdminsOtherThan(ctx, tx, id)
		if err != nil {
			return u, err
		}
		if n == 0 {
			return u, ErrLastAdmin
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET display_name=$2, role=$3, status=$4, updated_at=now() WHERE id=$1`, id, name, role, status); err != nil {
		return u, err
	}
	if status == "disabled" {
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return u, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return u, err
	}
	return s.AuthUserByID(ctx, id)
}

func (s *Store) DeleteAuthUser(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockAdmins(ctx, tx); err != nil {
		return err
	}
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if u.Role == "admin" && u.Status == "active" {
		n, err := activeAdminsOtherThan(ctx, tx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLastAdmin
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetPassword stores a new hash and clears failure counters and the lock. mustChange marks a temporary password.
func (s *Store) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2, must_change_password=$3, failed_logins=0, locked_until=NULL, updated_at=now() WHERE id=$1`, id, hash, mustChange)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// RecordLoginFailure increments the failure counter atomically; reaching the threshold locks the account and resets the counter.
func (s *Store) RecordLoginFailure(ctx context.Context, id string, threshold int, lockFor time.Duration, now time.Time) (locked bool, until *time.Time, err error) {
	var failed int
	var lu *time.Time
	// locked_until is assigned before failed_logins: MySQL evaluates SET assignments left to right.
	err = s.Pool.InTx(ctx, func(tx sqldb.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET locked_until = CASE WHEN failed_logins + 1 >= $2 THEN $3::timestamptz ELSE locked_until END,
			failed_logins = CASE WHEN failed_logins + 1 >= $2 THEN 0 ELSE failed_logins + 1 END, updated_at = now()
			WHERE id = $1`, id, threshold, now.Add(lockFor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return sqldb.ErrNoRows
		}
		return tx.QueryRow(ctx, `SELECT failed_logins, locked_until FROM users WHERE id = $1`, id).Scan(&failed, &lu)
	})
	if err != nil {
		return false, nil, err
	}
	if failed == 0 {
		return true, lu, nil
	}
	return false, nil, nil
}

func (s *Store) RecordLoginSuccess(ctx context.Context, id string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL, last_login_at=$2, updated_at=now() WHERE id=$1`, id, now)
	return err
}

func (s *Store) UnlockAuthUser(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL, updated_at=now() WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetTOTP(ctx context.Context, id string, enc []byte, confirmedAt time.Time, step int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET totp_secret_enc=$2, totp_confirmed_at=$3, totp_last_step=$4, updated_at=now() WHERE id=$1`, id, enc, confirmedAt, step)
	return err
}

func (s *Store) ClearTOTP(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET totp_secret_enc=NULL, totp_confirmed_at=NULL, totp_last_step=0, updated_at=now() WHERE id=$1`, id)
	return err
}

// AdvanceTOTPStep records a used time step; it returns false when the step is not newer than the last accepted one
// (replay, including concurrent attempts with the same code).
func (s *Store) AdvanceTOTPStep(ctx context.Context, id string, step int64) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET totp_last_step=$2 WHERE id=$1 AND totp_last_step < $2`, id, step)
	return err == nil && tag.RowsAffected() == 1, err
}

// ClearMFA removes TOTP, passkeys and recovery codes of a user and ends all their sessions.
func (s *Store) ClearMFA(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	for _, q := range []string{
		`UPDATE users SET totp_secret_enc=NULL, totp_confirmed_at=NULL, totp_last_step=0, updated_at=now() WHERE id=$1`,
		`DELETE FROM webauthn_credentials WHERE user_id=$1`,
		`DELETE FROM recovery_codes WHERE user_id=$1`,
		`DELETE FROM auth_challenges WHERE user_id=$1`,
		`DELETE FROM sessions WHERE user_id=$1`,
	} {
		if _, err = tx.Exec(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// --- passkeys

const credCols = `id, user_id::text, name, public_key, sign_count, clone_warning, aaguid, transports, backup_eligible, backup_state, created_at, last_used_at`

func scanCred(d sqldb.Dialect, r sqldb.Row) (WebAuthnCred, error) {
	var c WebAuthnCred
	var sc int64
	err := r.Scan(&c.ID, &c.UserID, &c.Name, &c.PublicKey, &sc, &c.CloneWarning, &c.AAGUID, d.ScanStrings(&c.Transports), &c.BackupEligible, &c.BackupState, &c.CreatedAt, &c.LastUsedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return c, ErrNotFound
	}
	c.SignCount = uint32(sc)
	if c.Transports == nil {
		c.Transports = []string{}
	}
	return c, err
}

func (s *Store) AddCredential(ctx context.Context, c WebAuthnCred) error {
	c = c.normalized()
	_, err := s.Pool.Exec(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, public_key, sign_count, aaguid, transports, backup_eligible, backup_state) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, c.UserID, c.Name, c.PublicKey, int64(c.SignCount), c.AAGUID, s.Dialect().StringsArg(c.Transports), c.BackupEligible, c.BackupState)
	if err != nil && s.Dialect().IsUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) Credentials(ctx context.Context, userID string) ([]WebAuthnCred, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+credCols+` FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebAuthnCred{}
	for rows.Next() {
		c, err := scanCred(s.Dialect(), rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Credential(ctx context.Context, userID string, id []byte) (WebAuthnCred, error) {
	return scanCred(s.Dialect(), s.Pool.QueryRow(ctx, `SELECT `+credCols+` FROM webauthn_credentials WHERE user_id=$1 AND id=$2`, userID, id))
}

// TouchCredential stores the counters after a successful assertion.
func (s *Store) TouchCredential(ctx context.Context, id []byte, signCount uint32, clone, backupState bool, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE webauthn_credentials SET sign_count=$2, clone_warning = CASE WHEN clone_warning=true OR $3=true THEN true ELSE false END, backup_state=$4, last_used_at=$5 WHERE id=$1`, id, int64(signCount), clone, backupState, now)
	return err
}

func (s *Store) RenameCredential(ctx context.Context, userID string, id []byte, name string) (WebAuthnCred, error) {
	if _, err := s.Pool.Exec(ctx, `UPDATE webauthn_credentials SET name=$3 WHERE user_id=$1 AND id=$2`, userID, id, name); err != nil {
		return WebAuthnCred{}, err
	}
	return s.Credential(ctx, userID, id)
}

func (s *Store) DeleteCredential(ctx context.Context, userID string, id []byte) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id=$1 AND id=$2`, userID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- recovery codes

// ReplaceRecoveryCodes invalidates every earlier code and stores the new hashes.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, hashes [][]byte) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1,$2)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RecoveryCodesRemaining(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id=$1 AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// ConsumeRecoveryCode compares the hash against every unused code in constant time and marks the match used exactly once.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID string, hash []byte, now time.Time) (bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT code_hash FROM recovery_codes WHERE user_id=$1 AND used_at IS NULL`, userID)
	if err != nil {
		return false, err
	}
	var match []byte
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return false, err
		}
		if subtle.ConstantTimeCompare(h, hash) == 1 {
			match = h
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || match == nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE recovery_codes SET used_at=$3 WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, userID, match, now)
	return err == nil && tag.RowsAffected() == 1, err
}

// --- sessions

const sessCols = `id, user_id::text, created_at, last_seen_at, expires_at, fresh_until, ip, user_agent, mfa, scope`

func scanSess(r sqldb.Row, extra ...any) (AuthSession, error) {
	var a AuthSession
	err := r.Scan(append([]any{&a.ID, &a.UserID, &a.CreatedAt, &a.LastSeenAt, &a.ExpiresAt, &a.FreshUntil, &a.IP, &a.UserAgent, &a.MFA, &a.Scope}, extra...)...)
	if errors.Is(err, sqldb.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) CreateSession(ctx context.Context, a AuthSession) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, fresh_until, ip, user_agent, mfa, scope) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		a.ID, a.UserID, a.CreatedAt, a.LastSeenAt, a.ExpiresAt, a.FreshUntil, a.IP, a.UserAgent, a.MFA, a.Scope)
	return err
}

// SessionWithUser loads a session and its user in one query.
func (s *Store) SessionWithUser(ctx context.Context, id string) (AuthSession, AuthUser, error) {
	var a AuthSession
	var u AuthUser
	err := s.Pool.QueryRow(ctx, `SELECT s.id, s.user_id::text, s.created_at, s.last_seen_at, s.expires_at, s.fresh_until, s.ip, s.user_agent, s.mfa, s.scope,
		u.id::text, u.username, u.display_name, u.role, u.status, COALESCE(u.password_hash,''), u.must_change_password, u.totp_secret_enc, u.totp_confirmed_at, u.totp_last_step, u.webauthn_handle, u.failed_logins, u.locked_until, u.last_login_at, u.created_at, u.updated_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = $1`, id).Scan(
		&a.ID, &a.UserID, &a.CreatedAt, &a.LastSeenAt, &a.ExpiresAt, &a.FreshUntil, &a.IP, &a.UserAgent, &a.MFA, &a.Scope,
		&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PasswordHash, &u.MustChangePassword, &u.TOTPSecretEnc, &u.TOTPConfirmedAt, &u.TOTPLastStep, &u.WebAuthnHandle, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return a, u, ErrNotFound
	}
	return a, u, err
}

func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=$2 WHERE id=$1`, id, now)
	return err
}

func (s *Store) MarkSessionFresh(ctx context.Context, id string, until time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET fresh_until=$2 WHERE id=$1`, id, until)
	return err
}

// PromoteSession turns a restricted enrolment session into a full one (after the first MFA is enrolled).
func (s *Store) PromoteSession(ctx context.Context, id, mfa string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET scope='full', mfa=$2 WHERE id=$1`, id, mfa)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, id)
	return err == nil && tag.RowsAffected() > 0, err
}

// DeleteUserSessions ends every session of a user except keepID (may be empty); it returns how many were removed.
func (s *Store) DeleteUserSessions(ctx context.Context, userID, keepID string) (int, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id <> $2`, userID, keepID)
	return int(tag.RowsAffected()), err
}

// ListSessions returns live sessions (not expired), newest first; userID may be empty for all users.
func (s *Store) ListSessions(ctx context.Context, userID string, now time.Time) ([]AuthSessionInfo, error) {
	q := `SELECT s.id, s.user_id::text, s.created_at, s.last_seen_at, s.expires_at, s.fresh_until, s.ip, s.user_agent, s.mfa, s.scope, u.username FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.expires_at > $1`
	args := []any{now}
	if userID != "" {
		q += ` AND s.user_id = $2`
		args = append(args, userID)
	}
	rows, err := s.Pool.Query(ctx, q+` ORDER BY s.last_seen_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthSessionInfo{}
	for rows.Next() {
		var a AuthSessionInfo
		if err := rows.Scan(&a.ID, &a.UserID, &a.CreatedAt, &a.LastSeenAt, &a.ExpiresAt, &a.FreshUntil, &a.IP, &a.UserAgent, &a.MFA, &a.Scope, &a.Username); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SessionByID(ctx context.Context, id string) (AuthSessionInfo, error) {
	var a AuthSessionInfo
	err := s.Pool.QueryRow(ctx, `SELECT s.id, s.user_id::text, s.created_at, s.last_seen_at, s.expires_at, s.fresh_until, s.ip, s.user_agent, s.mfa, s.scope, u.username FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id=$1`, id).
		Scan(&a.ID, &a.UserID, &a.CreatedAt, &a.LastSeenAt, &a.ExpiresAt, &a.FreshUntil, &a.IP, &a.UserAgent, &a.MFA, &a.Scope, &a.Username)
	if errors.Is(err, sqldb.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// PurgeExpiredAuth removes expired sessions, challenges and invitations.
func (s *Store) PurgeExpiredAuth(ctx context.Context, now time.Time) error {
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM sessions WHERE expires_at < $1`, now},
		{`DELETE FROM auth_challenges WHERE expires_at < $1`, now},
		{`DELETE FROM invitations WHERE expires_at < $1`, now.Add(-7 * 24 * time.Hour)},
	} {
		if _, err := s.Pool.Exec(ctx, q.sql, q.at); err != nil {
			return err
		}
	}
	return nil
}

// --- challenges

func (s *Store) CreateChallenge(ctx context.Context, c AuthChallenge) error {
	data := c.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO auth_challenges (id, kind, user_id, data, expires_at) VALUES ($1,$2,$3,$4,$5)`, c.ID, c.Kind, nullStr(c.UserID), string(data), c.ExpiresAt)
	return err
}

func scanChallenge(r sqldb.Row) (AuthChallenge, error) {
	var c AuthChallenge
	var uid *string
	var data []byte
	err := r.Scan(&c.ID, &c.Kind, &uid, &data, &c.Attempts, &c.ExpiresAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return c, ErrNotFound
	}
	if uid != nil {
		c.UserID = *uid
	}
	c.Data = data
	return c, err
}

const chalCols = `id, kind, user_id::text, data, attempts, expires_at`

// ConsumeChallenge deletes and returns a live challenge in one statement, so it can be used exactly once.
func (s *Store) ConsumeChallenge(ctx context.Context, id, kind string, now time.Time) (AuthChallenge, error) {
	c, err := s.PeekChallenge(ctx, id, kind, now)
	if err != nil {
		return c, err
	}
	// Exactly one concurrent caller deletes the row, and only that caller gets the challenge.
	tag, err := s.Pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id=$1 AND kind=$2 AND expires_at > $3`, id, kind, now)
	if err != nil {
		return AuthChallenge{}, err
	}
	if tag.RowsAffected() != 1 {
		return AuthChallenge{}, ErrNotFound
	}
	return c, nil
}

func (s *Store) PeekChallenge(ctx context.Context, id, kind string, now time.Time) (AuthChallenge, error) {
	return scanChallenge(s.Pool.QueryRow(ctx, `SELECT `+chalCols+` FROM auth_challenges WHERE id=$1 AND kind=$2 AND expires_at > $3`, id, kind, now))
}

// BumpChallenge counts a failed attempt and deletes the challenge once max attempts are used.
func (s *Store) BumpChallenge(ctx context.Context, id string, max int) (int, error) {
	var n int
	err := s.Pool.InTx(ctx, func(tx sqldb.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE auth_challenges SET attempts = attempts + 1 WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return sqldb.ErrNoRows
		}
		return tx.QueryRow(ctx, `SELECT attempts FROM auth_challenges WHERE id=$1`, id).Scan(&n)
	})
	if errors.Is(err, sqldb.ErrNoRows) {
		return max, nil
	}
	if err == nil && n >= max {
		_, err = s.Pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id=$1`, id)
	}
	return n, err
}

// DeleteChallenge removes a challenge; the bool says whether this call removed it (one-time success under concurrency).
func (s *Store) DeleteChallenge(ctx context.Context, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM auth_challenges WHERE id=$1`, id)
	return err == nil && tag.RowsAffected() == 1, err
}

// --- invitations

// CreateInvitation replaces any earlier invitation of the user.
func (s *Store) CreateInvitation(ctx context.Context, userID, tokenHash string, expires time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM invitations WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invitations (token_hash, user_id, expires_at) VALUES ($1,$2,$3)`, tokenHash, userID, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InvitationUser returns the invited user for a live (unused, unexpired) invitation.
func (s *Store) InvitationUser(ctx context.Context, tokenHash string, now time.Time) (AuthUser, Invitation, error) {
	var inv Invitation
	var uid string
	err := s.Pool.QueryRow(ctx, `SELECT token_hash, user_id::text, expires_at FROM invitations WHERE token_hash=$1 AND used_at IS NULL AND expires_at > $2`, tokenHash, now).Scan(&inv.TokenHash, &uid, &inv.ExpiresAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return AuthUser{}, inv, ErrNotFound
	}
	if err != nil {
		return AuthUser{}, inv, err
	}
	inv.UserID = uid
	u, err := s.AuthUserByID(ctx, uid)
	return u, inv, err
}

// AcceptInvitation marks the invitation used and stores the first credential atomically; a second use finds nothing.
func (s *Store) AcceptInvitation(ctx context.Context, tokenHash string, now time.Time, c WebAuthnCred) (AuthUser, error) {
	c = c.normalized()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AuthUser{}, err
	}
	defer tx.Rollback(context.Background())
	var uid string
	tag, err := tx.Exec(ctx, `UPDATE invitations SET used_at=$2 WHERE token_hash=$1 AND used_at IS NULL AND expires_at > $2`, tokenHash, now)
	if err != nil {
		return AuthUser{}, err
	}
	if tag.RowsAffected() != 1 {
		return AuthUser{}, ErrNotFound
	}
	if err = tx.QueryRow(ctx, `SELECT user_id::text FROM invitations WHERE token_hash=$1`, tokenHash).Scan(&uid); err != nil {
		return AuthUser{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, public_key, sign_count, aaguid, transports, backup_eligible, backup_state) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, uid, c.Name, c.PublicKey, int64(c.SignCount), c.AAGUID, tx.Dialect().StringsArg(c.Transports), c.BackupEligible, c.BackupState); err != nil {
		return AuthUser{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AuthUser{}, err
	}
	return s.AuthUserByID(ctx, uid)
}

// CountUsersWithoutPasskey counts active users who could not log in once password login is turned off.
func (s *Store) CountUsersWithoutPasskey(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users u WHERE u.status='active' AND NOT EXISTS (SELECT 1 FROM webauthn_credentials c WHERE c.user_id = u.id)`).Scan(&n)
	return n, err
}

// CountPasskeyUsers counts users that have at least one passkey (settings changes that invalidate passkeys).
func (s *Store) CountPasskeys(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials`).Scan(&n)
	return n, err
}

// DeleteAllCredentials wipes every passkey (used when the relying-party identity changes).
func (s *Store) DeleteAllCredentials(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM webauthn_credentials`)
	return err
}

// normalized replaces nil slices (stored columns are NOT NULL).
func (c WebAuthnCred) normalized() WebAuthnCred {
	if c.AAGUID == nil {
		c.AAGUID = []byte{}
	}
	if c.Transports == nil {
		c.Transports = []string{}
	}
	return c
}

// FinishWizard marks the setup wizard finished (idempotent); it only applies to an initialised system.
func (s *Store) FinishWizard(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE auth_settings SET wizard_finished=true, updated_at=now() WHERE id=1 AND setup_completed=true AND wizard_finished=false`)
	return err
}
