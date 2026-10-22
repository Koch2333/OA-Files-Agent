// oa-admin is the server-side break-glass tool for the account system. It talks to the database directly (the same
// OA_DATABASE_URL and secret key as the server) and audits every action as "oa-admin(cli)".
//
//	oa-admin setup-token | status | list-users | create-admin <user> | reset-password <user> | reset-mfa <user> |
//	         unlock <user> | disable-user <user> | enable-user <user>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: oa-admin <command> [username]

  setup-token            issue a new one-time setup token (only while the system is not initialised)
  status                 show initialisation state and account counts
  list-users             list accounts
  create-admin <user>    create an administrator with a temporary password (break-glass)
  reset-password <user>  set a new temporary password (shown once) and end the user's sessions
  reset-mfa <user>       remove TOTP, passkeys and recovery codes
  unlock <user>          clear the lockout
  disable-user <user>    disable the account (the last active administrator cannot be disabled)
  enable-user <user>     enable the account again

environment: OA_DATABASE_URL (required), OA_SECRET_KEY, OA_SPOOL_ROOT (locates data/secret.key like the server)`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	url := os.Getenv("OA_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "OA_DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer st.Close()
	svc := auth.New(st, openBox())
	if err := run(ctx, svc, os.Args[1], os.Args[2:]); err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) {
			fmt.Fprintln(os.Stderr, "error:", ae.Message)
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// openBox opens the server's secret key if it exists; the CLI never creates one (it does not handle TOTP secrets).
func openBox() *secrets.Box {
	spool := os.Getenv("OA_SPOOL_ROOT")
	if spool == "" {
		spool = "./data/spool"
	}
	abs, err := filepath.Abs(spool)
	if err != nil {
		return nil
	}
	file := filepath.Join(filepath.Dir(abs), "secret.key")
	if os.Getenv("OA_SECRET_KEY") == "" {
		if _, err := os.Stat(file); err != nil {
			return nil
		}
	}
	b, err := secrets.Open(os.Getenv("OA_SECRET_KEY"), file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: secret key unavailable:", err)
		return nil
	}
	return b
}

func need(args []string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", errors.New("this command needs exactly one username")
	}
	return strings.TrimSpace(args[0]), nil
}

func userByName(ctx context.Context, svc *auth.Service, name string) (store.AuthUser, error) {
	u, err := svc.Store.AuthUserByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return u, fmt.Errorf("user %q does not exist", name)
	}
	return u, err
}

func run(ctx context.Context, svc *auth.Service, cmd string, args []string) error {
	switch cmd {
	case "setup-token":
		tok, err := svc.EnsureSetupToken(ctx)
		if err != nil {
			return err
		}
		if tok == "" {
			return errors.New("the system is already initialised; no setup token is needed")
		}
		fmt.Println("SETUP TOKEN:", tok)
		fmt.Println("Any earlier token no longer works. Enter it in the setup wizard.")
		return nil
	case "status":
		set, err := svc.Store.AuthSettings(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("oa-admin %s\ninitialised: %v\nmode: %s\nmfa policy: %s\npassword login: %v\nrp id: %q\n", buildinfo.String(), set.SetupCompleted, set.Mode, set.MFAPolicy, set.AllowPasswordLogin, set.RPID)
		list, total, err := svc.Store.ListAuthUsers(ctx, store.UserFilter{Now: time.Now()}, store.Page{Page: 1, Size: 200})
		if err != nil {
			return err
		}
		admins, locked, disabled := 0, 0, 0
		for _, u := range list {
			if u.Role == "admin" && u.Status == "active" {
				admins++
			}
			if u.Status == "disabled" {
				disabled++
			}
			if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
				locked++
			}
		}
		fmt.Printf("users: %d (active admins: %d, disabled: %d, locked: %d)\n", total, admins, disabled, locked)
		return nil
	case "list-users":
		list, _, err := svc.Store.ListAuthUsers(ctx, store.UserFilter{Now: time.Now()}, store.Page{Page: 1, Size: 200})
		if err != nil {
			return err
		}
		if len(args) == 1 && args[0] == "--json" {
			type row struct {
				Username    string     `json:"username"`
				Role        string     `json:"role"`
				Status      string     `json:"status"`
				MFA         bool       `json:"mfa"`
				Locked      bool       `json:"locked"`
				LockedUntil *time.Time `json:"locked_until"`
			}
			rows := make([]row, 0, len(list))
			for _, u := range list {
				r := row{Username: u.Username, Role: u.Role, Status: u.Status, MFA: u.TOTPEnabled() || u.PasskeyCount > 0}
				if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
					r.Locked, r.LockedUntil = true, u.LockedUntil
				}
				rows = append(rows, r)
			}
			return json.NewEncoder(os.Stdout).Encode(rows)
		}
		fmt.Printf("%-24s %-9s %-9s %-4s %-5s %s\n", "USERNAME", "ROLE", "STATUS", "TOTP", "PASSK", "LOCKED UNTIL")
		for _, u := range list {
			lock := "-"
			if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
				lock = u.LockedUntil.Local().Format("2006-01-02 15:04:05")
			}
			fmt.Printf("%-24s %-9s %-9s %-4v %-5d %s\n", u.Username, u.Role, u.Status, u.TOTPEnabled(), u.PasskeyCount, lock)
		}
		return nil
	}
	name, err := need(args)
	if err != nil {
		return err
	}
	set, err := svc.Store.AuthSettings(ctx)
	if err != nil {
		return err
	}
	if !set.SetupCompleted && cmd != "create-admin" {
		return errors.New("the system is not initialised yet; use the setup wizard (oa-admin setup-token)")
	}
	switch cmd {
	case "create-admin":
		if !set.SetupCompleted {
			return errors.New("the system is not initialised yet; use the setup wizard (oa-admin setup-token)")
		}
		c, err := svc.CreateUser(ctx, auth.CLIActor, auth.NewUser{Username: name, DisplayName: name, Role: "admin", Credential: "password", BreakGlass: true})
		if err != nil {
			return err
		}
		fmt.Printf("created administrator %s\nTEMPORARY PASSWORD: %s\n(shown once; the password must be changed at first login)\n", name, c.Password)
		return nil
	case "reset-password":
		u, err := userByName(ctx, svc, name)
		if err != nil {
			return err
		}
		pw, err := svc.ResetPassword(ctx, auth.CLIActor, u.ID)
		if err != nil {
			return err
		}
		fmt.Printf("password reset for %s\nTEMPORARY PASSWORD: %s\n(shown once; all sessions of the user were ended; the password must be changed at next login)\n", name, pw)
		return nil
	case "reset-mfa":
		u, err := userByName(ctx, svc, name)
		if err != nil {
			return err
		}
		if _, err := svc.ResetMFA(ctx, auth.CLIActor, u.ID); err != nil {
			return err
		}
		fmt.Printf("TOTP, passkeys and recovery codes of %s were removed; sessions ended.\n", name)
		return nil
	case "unlock":
		u, err := userByName(ctx, svc, name)
		if err != nil {
			return err
		}
		if _, err := svc.UnlockUser(ctx, auth.CLIActor, u.ID); err != nil {
			return err
		}
		fmt.Printf("%s unlocked.\n", name)
		return nil
	case "disable-user", "enable-user":
		u, err := userByName(ctx, svc, name)
		if err != nil {
			return err
		}
		status := map[string]string{"disable-user": "disabled", "enable-user": "active"}[cmd]
		if _, err := svc.UpdateUser(ctx, auth.CLIActor, u.ID, auth.UserUpdate{Status: &status}); err != nil {
			return err
		}
		fmt.Printf("%s is now %s.\n", name, status)
		return nil
	}
	usage()
	return nil
}
