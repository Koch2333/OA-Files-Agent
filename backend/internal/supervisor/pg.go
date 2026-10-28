package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Postgres is an embedded PostgreSQL instance owned by this process.
type Postgres struct {
	l    Layout
	Port int
	URL  string
}

func (l Layout) pgBin(name string) string { return filepath.Join(l.PGDir, "bin", exeName(name)) }
func (l Layout) pgData() string           { return filepath.Join(l.Data, "pg") }

func runPG(ctx context.Context, l Layout, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, l.pgBin(bin), args...)
	cmd.Env = os.Environ()
	hide(cmd)
	// Write to a file, not a pipe: pg_ctl start leaves postmaster running with inherited handles, so a pipe would never reach EOF.
	f, ferr := os.CreateTemp("", "oa-pg-*.log")
	if ferr != nil {
		return "", ferr
	}
	defer os.Remove(f.Name())
	cmd.Stdout, cmd.Stderr = f, f
	err := cmd.Run()
	f.Close()
	out, _ := os.ReadFile(f.Name())
	return string(out), err
}

// StartPostgres initialises the data directory on first run (random superuser password kept in data/pg.secret),
// starts the server on a free loopback port and makes sure the `oa` database exists.
func StartPostgres(ctx context.Context, l Layout) (*Postgres, error) {
	if _, err := os.Stat(l.pgBin("pg_ctl")); err != nil {
		return nil, fmt.Errorf("embedded PostgreSQL not found in %s", l.PGDir)
	}
	trackLayout(l)
	secretPath := filepath.Join(l.Data, "pg.secret")
	if err := os.MkdirAll(l.Data, 0o700); err != nil {
		return nil, err
	}
	data := l.pgData()
	if _, err := os.Stat(filepath.Join(data, "PG_VERSION")); errors.Is(err, os.ErrNotExist) {
		pw := randomHex(16)
		pwf := filepath.Join(l.Data, "pg.pwfile")
		if err := os.WriteFile(pwf, []byte(pw), 0o600); err != nil {
			return nil, err
		}
		defer os.Remove(pwf)
		args := []string{"-D", data, "-U", "oa", "--pwfile=" + pwf, "--auth=scram-sha-256", "-E", "UTF8", "--locale=C"}
		share, cleanShare, serr := initdbShareDir(l)
		if serr != nil {
			return nil, fmt.Errorf("preparing initdb for a non-ASCII install path failed: %v", serr)
		}
		defer cleanShare()
		if share != "" {
			log.Printf("install path has non-ASCII characters; initdb reads its files from %s", share)
			args = append(args, "-L", share)
		}
		if out, err := runPG(ctx, l, "initdb", args...); err != nil {
			return nil, fmt.Errorf("initdb failed: %v: %s", err, tail(out))
		}
		if err := os.WriteFile(secretPath, []byte(pw), 0o600); err != nil {
			return nil, err
		}
	}
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("database password file missing (%s): delete %s to recreate the database", secretPath, data)
	}
	pw := strings.TrimSpace(string(raw))
	port, err := FreePort()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(l.Logs, "postgres.log")
	_ = os.MkdirAll(l.Logs, 0o700)
	// A stale postmaster.pid from a crash is handled by pg_ctl; -w waits until ready.
	// A server left behind by a previous run that was killed (crash, taskkill /F) still owns the data directory and the log
	// file: stop it first (fast mode) so the new instance can start. pg_ctl status exits 0 when a server is running.
	if _, serr := runPG(ctx, l, "pg_ctl", "-D", data, "status"); serr == nil {
		log.Print("stopping a leftover embedded PostgreSQL from a previous run …")
		if out, err := runPG(ctx, l, "pg_ctl", "-D", data, "-m", "fast", "-w", "-t", "60", "stop"); err != nil {
			return nil, fmt.Errorf("a previous embedded PostgreSQL is still running and could not be stopped: %v: %s", err, tail(out))
		}
	}
	out, err := runPG(ctx, l, "pg_ctl", "-D", data, "-l", logPath, "-w", "-t", "60", "-o", fmt.Sprintf("-p %d -c listen_addresses=127.0.0.1 -c unix_socket_directories=", port), "start")
	if err != nil {
		return nil, fmt.Errorf("pg_ctl start failed: %v: %s", err, tail(out))
	}
	p := &Postgres{l: l, Port: port}
	base := fmt.Sprintf("postgres://oa:%s@127.0.0.1:%d/", url.QueryEscape(pw), port)
	if err := ensureDB(ctx, base+"postgres?sslmode=disable", "oa"); err != nil {
		p.Stop()
		return nil, err
	}
	p.URL = base + "oa?sslmode=disable"
	return p, nil
}

func ensureDB(ctx context.Context, adminURL, name string) error {
	var conn *pgx.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = pgx.Connect(ctx, adminURL)
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		return errors.New("cannot connect to embedded PostgreSQL")
	}
	defer conn.Close(context.Background())
	var exists bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		_, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" ENCODING 'UTF8' TEMPLATE template0")
	}
	return err
}

// Stop shuts the server down gracefully (fast mode).
func (p *Postgres) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runPG(ctx, p.l, "pg_ctl", "-D", p.l.pgData(), "-m", "fast", "-w", "stop")
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		return s[len(s)-600:]
	}
	return s
}
