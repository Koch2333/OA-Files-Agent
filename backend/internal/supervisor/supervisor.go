// Package supervisor runs the helper processes of a single-machine ("standalone") install: the Python
// document worker and an embedded PostgreSQL. Everything is loopback-only and stopped when the API exits.
package supervisor

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/diag"
)

// Layout is the standalone directory structure relative to the executable.
type Layout struct {
	Root       string // directory containing oa-agent.exe
	Web        string
	WorkerExe  string
	PGDir      string // contains bin/{initdb,pg_ctl,postgres}
	Migrations string
	Data       string
	Spool      string
	Archive    string
	Logs       string
}

func NewLayout(root string) Layout {
	d := filepath.Join(root, "data")
	if v := os.Getenv("OA_DATA_DIR"); v != "" { // writable data outside the program directory (per-user desktop install)
		d = v
	}
	return Layout{
		Root: root, Web: filepath.Join(root, "web"), WorkerExe: filepath.Join(root, "worker", exeName("document-worker")),
		PGDir: filepath.Join(root, "pgsql"), Migrations: filepath.Join(root, "migrations"),
		Data: d, Spool: filepath.Join(d, "spool"), Archive: filepath.Join(d, "archive"), Logs: filepath.Join(d, "logs"),
	}
}

func exeName(n string) string {
	if isWindows() {
		return n + ".exe"
	}
	return n
}

// LoadEnvFile reads KEY=VALUE lines (# comments) from path and sets variables that are not already set.
// It exists so non-technical users can edit `oa-agent.env` next to the executable.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"`)
		if !ok || k == "" || !strings.HasPrefix(k, "OA_") {
			continue
		}
		if os.Getenv(k) == "" && v != "" {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// FreePort returns a currently free loopback TCP port.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func logFile(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// Worker keeps the document worker process alive (restart with backoff) until Stop.
type Worker struct {
	URL   string
	Token string

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// StartWorker launches the worker on a free loopback port with a fresh random token.
func StartWorker(ctx context.Context, l Layout) (*Worker, error) {
	if _, err := os.Stat(l.WorkerExe); err != nil {
		return nil, fmt.Errorf("worker executable not found: %s", l.WorkerExe)
	}
	port, err := FreePort()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.Spool, 0o700); err != nil {
		return nil, err
	}
	w := &Worker{URL: fmt.Sprintf("http://127.0.0.1:%d", port), Token: randomHex(24)}
	trackWorker(w)
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	archiveRoot := os.Getenv("OA_ARCHIVE_ROOT")
	if archiveRoot == "" {
		archiveRoot = l.Archive
	}
	env := append(os.Environ(), "OA_WORKER_TOKEN="+w.Token, "OA_SPOOL_ROOT="+l.Spool, fmt.Sprintf("OA_WORKER_PORT=%d", port), "OA_ALLOWED_ROOTS="+archiveRoot) // the worker may preview (read-only) the archive in addition to the spool
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		// diag.Loop keeps the supervision alive even if something in this loop panics (recorded as a report).
		diag.Loop(cctx, "worker.supervise", func(cctx context.Context) {
			backoff := time.Second
			for cctx.Err() == nil {
				lf, _ := logFile(l.Logs, "worker.log")
				cmd := exec.CommandContext(cctx, l.WorkerExe)
				cmd.Env, cmd.Dir = env, filepath.Dir(l.WorkerExe)
				if lf != nil {
					cmd.Stdout, cmd.Stderr = lf, lf
				}
				hide(cmd)
				start := time.Now()
				runErr := cmd.Run()
				if lf != nil {
					lf.Close()
				}
				if cctx.Err() == nil {
					msg := "文档解析 Worker 进程意外退出，将自动重启"
					if runErr != nil {
						msg += "：" + runErr.Error()
					}
					diag.Report(context.Background(), diag.Entry{Source: "worker", Code: "WORKER_EXITED", Message: msg, Details: map[string]any{"ran_seconds": int(time.Since(start).Seconds()), "restart_in_seconds": int(backoff.Seconds())}})
				}
				if time.Since(start) > 30*time.Second {
					backoff = time.Second
				}
				select {
				case <-cctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
			}
		})
	}()
	return w, nil
}

func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}
