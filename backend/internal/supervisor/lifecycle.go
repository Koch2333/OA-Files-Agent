package supervisor

import (
	"io"
	"log"
	"os"
	"sync"
	"time"

	"oa-agent/backend/internal/diag"
)

// Desktop shell support: when OA_EXIT_ON_STDIN_EOF=1 the process stops its helpers and exits as soon as its
// standard input is closed. A GUI parent (the Electron shell) cannot deliver Ctrl-C to a console-less child on
// Windows, and TerminateProcess would leave postgres.exe / the worker running, so it simply closes the pipe (which
// also happens automatically if the parent crashes).

var (
	lcMu      sync.Mutex
	lcLayouts []Layout
	lcWorkers []*Worker
)

func trackLayout(l Layout) {
	lcMu.Lock()
	lcLayouts = append(lcLayouts, l)
	lcMu.Unlock()
}

func trackWorker(w *Worker) {
	lcMu.Lock()
	lcWorkers = append(lcWorkers, w)
	lcMu.Unlock()
}

func init() {
	if os.Getenv("OA_EXIT_ON_STDIN_EOF") != "1" {
		return
	}
	diag.Go("supervisor.stdin_watch", func() {
		_, _ = io.Copy(io.Discard, os.Stdin) // returns at EOF / broken pipe
		log.Print("stdin closed by parent: stopping helpers and exiting")
		stopAllAndExit()
	})
}

func stopAllAndExit() {
	lcMu.Lock()
	ws := append([]*Worker(nil), lcWorkers...)
	ls := append([]Layout(nil), lcLayouts...)
	lcMu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, w := range ws {
			w.Stop()
		}
		// pg_ctl stop is idempotent; it also covers a server that was still starting up.
		for _, l := range ls {
			(&Postgres{l: l}).Stop()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(70 * time.Second):
	}
	diag.ReleaseRunLock() // a graceful exit: the next start must not report an unclean one
	os.Exit(0)
}
