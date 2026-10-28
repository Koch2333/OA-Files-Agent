package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oa-agent/backend/internal/imports"
)

func mkUser(t *testing.T, s *Store, name, role string) AuthUser {
	t.Helper()
	id, _ := imports.NewID()
	u := AuthUser{ID: id, Username: name, DisplayName: name, Role: role, Status: "active", PasswordHash: "x", WebAuthnHandle: []byte(id + id)}
	if err := s.CreateAuthUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func parallel(n int, f func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			f(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestAuthSettingsDefaultsAndSetupToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, err := s.AuthSettings(ctx)
	if err != nil || a.SetupCompleted || a.Mode != "enterprise" || a.MFAPolicy != "admins" || !a.AllowPasswordLogin || a.SessionIdleMinutes != 60 || a.SessionMaxHours != 12 || a.PasswordMinLength != 8 || a.LockoutThreshold != 5 || a.LockoutMinutes != 15 {
		t.Fatalf("%+v %v", a, err)
	}
	if err := s.SetSetupToken(ctx, "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetupToken(ctx, "hash2"); err != nil { // a new start replaces the token
		t.Fatal(err)
	}
	a, _ = s.AuthSettings(ctx)
	if a.SetupTokenHash != "hash2" {
		t.Fatal("token not replaced")
	}
}

func TestCompleteSetupIsAtomicAndSingleWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_ = s.SetSetupToken(ctx, "good")
	set, _ := s.AuthSettings(ctx)
	verify := func(h string) bool { return h == "good" }
	// a wrong token fails and leaves nothing behind
	id, _ := imports.NewID()
	bad := AuthUser{ID: id, Username: "ghost", Role: "admin", Status: "active", WebAuthnHandle: []byte("h-ghost")}
	if err := s.CompleteSetup(ctx, SetupInput{Settings: set, Admin: &bad, VerifyToken: func(string) bool { return false }}); !errors.Is(err, ErrBadSetupToken) {
		t.Fatalf("want ErrBadSetupToken, got %v", err)
	}
	if _, err := s.AuthUserByName(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed setup must not create the admin")
	}
	var wins, already atomic.Int32
	parallel(12, func(i int) {
		uid, _ := imports.NewID()
		adm := AuthUser{ID: uid, Username: fmt.Sprintf("admin%d", i), Role: "admin", Status: "active", WebAuthnHandle: []byte(fmt.Sprintf("handle-%d", i))}
		switch err := s.CompleteSetup(ctx, SetupInput{Settings: set, Admin: &adm, VerifyToken: verify}); {
		case err == nil:
			wins.Add(1)
		case errors.Is(err, ErrAlreadySetup):
			already.Add(1)
		default:
			t.Errorf("unexpected: %v", err)
		}
	})
	if wins.Load() != 1 || already.Load() != 11 {
		t.Fatalf("wins=%d already=%d", wins.Load(), already.Load())
	}
	users, total, _ := s.ListAuthUsers(ctx, UserFilter{Now: time.Now()}, Page{})
	if total != 1 || len(users) != 1 {
		t.Fatalf("exactly one admin expected, got %d", total)
	}
	a, _ := s.AuthSettings(ctx)
	if !a.SetupCompleted || a.SetupTokenHash != "" {
		t.Fatal("setup state / token not cleared")
	}
	if err := s.SetSetupToken(ctx, "again"); !errors.Is(err, ErrAlreadySetup) {
		t.Fatal("token can no longer be issued")
	}
}

func TestLastAdminProtectionUnderConcurrency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, b := mkUser(t, s, "adm-a", "admin"), mkUser(t, s, "adm-b", "admin")
	viewer := "viewer"
	var blocked, done atomic.Int32
	parallel(2, func(i int) {
		id := []string{a.ID, b.ID}[i]
		_, err := s.UpdateAuthUser(ctx, id, UserPatch{Role: &viewer})
		switch {
		case err == nil:
			done.Add(1)
		case errors.Is(err, ErrLastAdmin):
			blocked.Add(1)
		default:
			t.Error(err)
		}
	})
	if done.Load() != 1 || blocked.Load() != 1 {
		t.Fatalf("exactly one demotion may succeed: done=%d blocked=%d", done.Load(), blocked.Load())
	}
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND status='active'`).Scan(&n)
	if n != 1 {
		t.Fatalf("active admins = %d", n)
	}
	// disabling and deleting the last one is refused as well
	last := a
	if err := s.DeleteAuthUser(ctx, a.ID); err == nil {
		last = b
	}
	disabled := "disabled"
	if _, err := s.UpdateAuthUser(ctx, last.ID, UserPatch{Status: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disable last admin: %v", err)
	}
	if err := s.DeleteAuthUser(ctx, last.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
	// with a second admin the first can go
	c := mkUser(t, s, "adm-c", "admin")
	if err := s.DeleteAuthUser(ctx, last.ID); err != nil {
		t.Fatal(err)
	}
	_ = c
}

func TestUserNameUniqueCaseInsensitive(t *testing.T) {
	s := testStore(t)
	mkUser(t, s, "Alice", "viewer")
	id, _ := imports.NewID()
	err := s.CreateAuthUser(context.Background(), AuthUser{ID: id, Username: "aLICE", Role: "viewer", WebAuthnHandle: []byte("zz")})
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("%v", err)
	}
	if u, err := s.AuthUserByName(context.Background(), "ALICE"); err != nil || u.Username != "Alice" {
		t.Fatal("lookup is case-insensitive")
	}
}

func TestChallengeIsOneTimeAndExpires(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateChallenge(ctx, AuthChallenge{ID: "c1", Kind: "login_mfa", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var got atomic.Int32
	parallel(10, func(int) {
		if _, err := s.ConsumeChallenge(ctx, "c1", "login_mfa", now); err == nil {
			got.Add(1)
		}
	})
	if got.Load() != 1 {
		t.Fatalf("challenge consumed %d times", got.Load())
	}
	_ = s.CreateChallenge(ctx, AuthChallenge{ID: "c2", Kind: "login_mfa", ExpiresAt: now.Add(time.Minute)})
	if _, err := s.ConsumeChallenge(ctx, "c2", "webauthn_login", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("kind must match")
	}
	if _, err := s.ConsumeChallenge(ctx, "c2", "login_mfa", now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired challenge consumed")
	}
	_ = s.CreateChallenge(ctx, AuthChallenge{ID: "c3", Kind: "login_mfa", ExpiresAt: now.Add(time.Minute)})
	for i := 0; i < 5; i++ {
		_, _ = s.BumpChallenge(ctx, "c3", 5)
	}
	if _, err := s.PeekChallenge(ctx, "c3", "login_mfa", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("challenge must be deleted after max attempts")
	}
}

func TestRecoveryCodeConsumedOnceAndReplacedCodesDie(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "rc-user", "viewer")
	h := [][]byte{[]byte("hash-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("hash-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")}
	_ = s.ReplaceRecoveryCodes(ctx, u.ID, h)
	var wins atomic.Int32
	parallel(8, func(int) {
		if ok, err := s.ConsumeRecoveryCode(ctx, u.ID, h[0], time.Now()); err == nil && ok {
			wins.Add(1)
		}
	})
	if wins.Load() != 1 {
		t.Fatalf("code used %d times", wins.Load())
	}
	if n, _ := s.RecoveryCodesRemaining(ctx, u.ID); n != 1 {
		t.Fatalf("remaining %d", n)
	}
	_ = s.ReplaceRecoveryCodes(ctx, u.ID, [][]byte{[]byte("hash-cccccccccccccccccccccccccccccccc")})
	if ok, _ := s.ConsumeRecoveryCode(ctx, u.ID, h[1], time.Now()); ok {
		t.Fatal("regenerating must invalidate the old codes")
	}
}

func TestTOTPStepAdvancesOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "totp-user", "viewer")
	var wins atomic.Int32
	parallel(8, func(int) {
		if ok, _ := s.AdvanceTOTPStep(ctx, u.ID, 100); ok {
			wins.Add(1)
		}
	})
	if wins.Load() != 1 {
		t.Fatalf("same step accepted %d times", wins.Load())
	}
	if ok, _ := s.AdvanceTOTPStep(ctx, u.ID, 99); ok {
		t.Fatal("older step accepted")
	}
	if ok, _ := s.AdvanceTOTPStep(ctx, u.ID, 101); !ok {
		t.Fatal("newer step refused")
	}
}

func TestInvitationOneTimeAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "invitee", "viewer")
	now := time.Now()
	if err := s.CreateInvitation(ctx, u.ID, "th1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InvitationUser(ctx, "th1", now); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	parallel(6, func(i int) {
		c := WebAuthnCred{ID: []byte(fmt.Sprintf("cred-%d", i)), Name: "pk", PublicKey: []byte("k"), Transports: []string{}}
		if _, err := s.AcceptInvitation(ctx, "th1", now, c); err == nil {
			wins.Add(1)
		}
	})
	if wins.Load() != 1 {
		t.Fatalf("invitation accepted %d times", wins.Load())
	}
	if creds, _ := s.Credentials(ctx, u.ID); len(creds) != 1 {
		t.Fatalf("credentials: %d", len(creds))
	}
	if _, _, err := s.InvitationUser(ctx, "th1", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("used invitation still visible")
	}
	_ = s.CreateInvitation(ctx, u.ID, "th2", now.Add(time.Hour))
	if _, _, err := s.InvitationUser(ctx, "th1", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("old")
	}
	if _, _, err := s.InvitationUser(ctx, "th2", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired invitation visible")
	}
	if _, err := s.AcceptInvitation(ctx, "th2", now.Add(2*time.Hour), WebAuthnCred{ID: []byte("late"), PublicKey: []byte("k")}); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired invitation accepted")
	}
	// issuing a new invitation replaces the old one
	_ = s.CreateInvitation(ctx, u.ID, "th3", now.Add(time.Hour))
	if _, _, err := s.InvitationUser(ctx, "th2", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("replaced invitation still valid")
	}
}

func TestLoginFailureLocksAndSuccessClears(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "lock-user", "viewer")
	now := time.Now()
	for i := 1; i <= 4; i++ {
		if locked, _, err := s.RecordLoginFailure(ctx, u.ID, 5, 15*time.Minute, now); err != nil || locked {
			t.Fatalf("attempt %d: locked=%v %v", i, locked, err)
		}
	}
	locked, until, _ := s.RecordLoginFailure(ctx, u.ID, 5, 15*time.Minute, now)
	if !locked || until == nil || until.Before(now.Add(14*time.Minute)) {
		t.Fatal("fifth failure must lock for the configured time")
	}
	got, _ := s.AuthUserByID(ctx, u.ID)
	if got.LockedUntil == nil || got.FailedLogins != 0 {
		t.Fatalf("%+v", got)
	}
	_ = s.UnlockAuthUser(ctx, u.ID)
	_, _, _ = s.RecordLoginFailure(ctx, u.ID, 5, time.Minute, now)
	_ = s.RecordLoginSuccess(ctx, u.ID, now)
	got, _ = s.AuthUserByID(ctx, u.ID)
	if got.LockedUntil != nil || got.FailedLogins != 0 || got.LastLoginAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestSessionsAndPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "sess-user", "viewer")
	now := time.Now()
	mk := func(id string, exp time.Time) {
		if err := s.CreateSession(ctx, AuthSession{ID: id, UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: exp, FreshUntil: now, MFA: "none", Scope: "full"}); err != nil {
			t.Fatal(err)
		}
	}
	mk("s-live", now.Add(time.Hour))
	mk("s-old", now.Add(-time.Hour))
	list, _ := s.ListSessions(ctx, u.ID, now)
	if len(list) != 1 || list[0].ID != "s-live" || list[0].Username != "sess-user" {
		t.Fatalf("%+v", list)
	}
	if err := s.PurgeExpiredAuth(ctx, now); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, _, err := s.SessionWithUser(ctx, "s-old"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session not purged")
	}
	mk("s-2", now.Add(time.Hour))
	if n, _ := s.DeleteUserSessions(ctx, u.ID, "s-live"); n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if _, _, err := s.SessionWithUser(ctx, "s-live"); err != nil {
		t.Fatal("kept session removed")
	}
}
