package main

// db-check / db-init: validate an EXISTING database server (and, after a second explicit approval, create the OA Agent
// schema) BEFORE a deployment exists. They run on the connection settings in the environment (OA_DB_PROVIDER, OA_DB_HOST,
// OA_DB_PORT, OA_DB_NAME, OA_DB_USER, OA_DB_SCHEMA, OA_DB_TLS_MODE, OA_DB_TLS_CA_FILE ...) and never touch the account
// system. The password comes from OA_DB_PASSWORD / OA_DB_PASSWORD_FILE or, preferably, from standard input
// (--password-stdin), so it never appears in a process argument list. All database logic is the dbadmin package that
// also serves Settings -> Database; nothing is re-implemented here.
//
// Output is line oriented and tab separated so the installer can show it (every line starts with a keyword):
//
//	CHECK <status> <id> <title> <detail>   one line of the checklist (status: pass warn fail info)
//	INFO <key> <value>                     provider, server_version, isolation, schema
//	PLAN ...  STEP ...  DDL ...            db-init preview (DDL lines only with --show-ddl)
//	RESULT <ok|fail> <reason>              last line; exit status 0 = ok, 1 = checks failed, 2 = bad usage/settings

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"oa-agent/backend/internal/dbadmin"
	"oa-agent/backend/internal/dbconfig"
	"oa-agent/backend/internal/sqldb"
)

const cliActor = "oa-admin(cli)"

// english is set by runDB from OA_LANG: the checklist texts are then translated (see dbcmd_en.go).
var english bool

// tsv writes one tab separated line; tabs and line breaks inside fields are flattened to spaces.
func tsv(w io.Writer, fields ...string) {
	for i, f := range fields {
		f = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(f)
		fields[i] = f
	}
	fmt.Fprintln(w, strings.Join(fields, "\t"))
}

// dbEnv returns getenv with the password taken from stdin when asked (and never from the file option then).
func dbEnv(getenv func(string) string, pw string, fromStdin bool) func(string) string {
	if !fromStdin {
		return getenv
	}
	return func(k string) string {
		switch k {
		case "OA_DB_PASSWORD":
			return pw
		case "OA_DB_PASSWORD_FILE", "OA_DB_DSN", "OA_DATABASE_URL":
			return "" // settings come from the discrete variables only
		}
		return getenv(k)
	}
}

func printReport(w io.Writer, rep dbadmin.Report) {
	for _, c := range rep.Checks {
		title, detail := localize(english, c.ID, c.Title, c.Detail)
		tsv(w, "CHECK", c.Status, c.ID, title, detail)
	}
	tsv(w, "INFO", "provider", rep.Provider)
	tsv(w, "INFO", "server_version", rep.ServerVersion)
	tsv(w, "INFO", "isolation", rep.Isolation)
	tsv(w, "INFO", "schema", rep.Schema.State)
}

// runDB implements db-check and db-init and returns the process exit status.
func runDB(cmd string, args []string, in io.Reader, out, errw io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	passStdin := fs.Bool("password-stdin", false, "read the database password (first line) from standard input")
	migrations := fs.String("migrations", "", "migrations directory (default $OA_MIGRATIONS_DIR or /app/migrations)")
	apply := fs.Bool("apply", false, "db-init: apply the previewed DDL (needs --plan)")
	plan := fs.String("plan", "", "db-init --apply: the PLAN fingerprint printed by the preview the administrator approved")
	showDDL := fs.Bool("show-ddl", false, "db-init preview: print every DDL statement")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(errw, "%s: unexpected argument %q\n", cmd, fs.Arg(0))
		return 2
	}
	pw := ""
	if *passStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(errw, cmd+": cannot read the password from standard input")
			return 2
		}
		pw = strings.TrimRight(line, "\r\n")
		if pw == "" {
			fmt.Fprintln(errw, cmd+": the password read from standard input is empty")
			return 2
		}
	}
	english = strings.HasPrefix(strings.ToLower(getenv("OA_LANG")), "en")
	cfg, ok, err := dbconfig.FromEnv(dbEnv(getenv, pw, *passStdin))
	if err != nil {
		fmt.Fprintln(errw, cmd+": "+sqldb.SafeError(err, cfg))
		tsv(out, "RESULT", "fail", "invalid settings")
		return 2
	}
	if !ok {
		fmt.Fprintln(errw, cmd+": no database settings: set OA_DB_PROVIDER, OA_DB_HOST, OA_DB_NAME, OA_DB_USER and the password (--password-stdin or OA_DB_PASSWORD)")
		return 2
	}
	if cfg.Password == "" {
		fmt.Fprintln(errw, cmd+": no database password: use --password-stdin, OA_DB_PASSWORD or OA_DB_PASSWORD_FILE")
		return 2
	}
	dir := *migrations
	if dir == "" {
		dir = getenv("OA_MIGRATIONS_DIR")
	}
	if dir == "" {
		dir = "/app/migrations"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if cmd == "db-check" {
		return dbCheck(ctx, cfg, out)
	}
	return dbInit(ctx, cfg, dir, *apply, *plan, *showDDL, out, errw)
}

func dbCheck(ctx context.Context, cfg sqldb.Config, out io.Writer) int {
	octx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d, err := sqldb.Open(octx, cfg)
	if err != nil {
		tsv(out, "CHECK", "fail", "connect", "connect", sqldb.SafeError(err, cfg))
		tsv(out, "INFO", "provider", string(cfg.Provider))
		tsv(out, "RESULT", "fail", "cannot connect")
		return 1
	}
	defer d.Close()
	rep := dbadmin.Inspect(ctx, d)
	printReport(out, rep)
	switch {
	case !rep.OK:
		tsv(out, "RESULT", "fail", "one or more checks failed")
		return 1
	case rep.Isolation == dbadmin.IsoNotIsolated:
		tsv(out, "RESULT", "fail", "the target is not isolated")
		return 1
	}
	tsv(out, "RESULT", "ok", "")
	return 0
}

// fingerprint binds an approval to the exact DDL that was previewed.
func fingerprint(pv dbadmin.InitPreview) string {
	h := sha256.New()
	for _, s := range pv.Steps {
		fmt.Fprintf(h, "%d|%s|%v\n", s.Version, s.Name, s.Destructive)
		for _, st := range s.Statements {
			h.Write([]byte(st))
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func dbInit(ctx context.Context, cfg sqldb.Config, dir string, apply bool, approved string, showDDL bool, out, errw io.Writer) int {
	svc := &dbadmin.Service{MigrationsDir: dir}
	svc.SetDirect(cfg)
	pv, err := svc.PlanInit(ctx, "direct", cliActor)
	if err != nil {
		var ve *dbadmin.ValidationError
		if errors.As(err, &ve) {
			printReport(out, ve.Report)
		}
		tsv(out, "RESULT", "fail", sqldb.SafeError(err, cfg))
		return 1
	}
	for _, c := range pv.Checks {
		if c.Status == dbadmin.Fail {
			for _, x := range pv.Checks {
				t, d := localize(english, x.ID, x.Title, x.Detail)
				tsv(out, "CHECK", x.Status, x.ID, t, d)
			}
			tsv(out, "RESULT", "fail", "the target does not pass the checklist: run db-check first")
			return 1
		}
	}
	fp := fingerprint(pv)
	tsv(out, "PLAN", "state="+pv.State, fmt.Sprintf("migrations=%d", len(pv.Steps)), fmt.Sprintf("statements=%d", pv.Statements),
		fmt.Sprintf("destructive=%v", pv.Destructive), fmt.Sprintf("transactional=%v", pv.Transactional), "fingerprint="+fp)
	for _, s := range pv.Steps {
		tsv(out, "STEP", fmt.Sprint(s.Version), s.Name, fmt.Sprint(len(s.Statements)))
		if showDDL {
			for _, st := range s.Statements {
				tsv(out, "DDL", strings.TrimSpace(st))
			}
		}
	}
	if len(pv.Steps) == 0 {
		tsv(out, "RESULT", "ok", "nothing to apply: the schema is already current")
		return 0
	}
	if pv.Destructive {
		tsv(out, "RESULT", "fail", "the plan contains destructive steps; apply it from Settings -> Database or oa-agent migrate with explicit approval")
		return 1
	}
	if !apply {
		tsv(out, "RESULT", "ok", "preview only: nothing was changed (approve with --apply --plan "+fp+")")
		return 0
	}
	if approved != fp {
		fmt.Fprintln(errw, "db-init: --plan does not match the DDL that would run now; preview again and approve the new plan")
		tsv(out, "RESULT", "fail", "plan fingerprint mismatch")
		return 1
	}
	if _, err := svc.Initialize(ctx, "direct", pv.Token, cliActor); err != nil {
		var ve *dbadmin.ValidationError
		if errors.As(err, &ve) {
			printReport(out, ve.Report)
		}
		tsv(out, "RESULT", "fail", sqldb.SafeError(err, cfg))
		return 1
	}
	tsv(out, "RESULT", "ok", fmt.Sprintf("applied %d migration(s)", len(pv.Steps)))
	return 0
}

// isDBCommand reports whether cmd runs without an initialised database (handled before the store is opened).
func isDBCommand(cmd string) bool { return cmd == "db-check" || cmd == "db-init" }

func dbMain(cmd string, args []string) {
	os.Exit(runDB(cmd, args, os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
