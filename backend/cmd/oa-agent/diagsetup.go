package main

import (
	"context"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/diag"
)

// initDiag points diag at the log directory, takes the run lock and, when the previous run did not exit gracefully,
// records a supervisor error report (kept in memory until the database is attached). The returned function releases
// the lock and must be deferred: it runs on every graceful exit (signal, shutdown), never after a crash or kill.
func initDiag(dir string) func() {
	diag.SetLogDir(dir)
	prev, release, err := diag.AcquireRunLock(dir, nil)
	if err != nil {
		log.Printf("run lock unavailable (%v): an unclean exit will not be detected", err)
		return func() {}
	}
	diag.CaptureRuntimeCrashes(dir) // after AcquireRunLock, which consumes the previous run's dump
	switch {
	case prev.Unclean:
		log.Printf("WARNING: the previous run ended uncleanly (pid %d, started %s); crash files: %v", prev.Info.PID, prev.Info.StartedAt.Format(time.RFC3339), prev.CrashFiles)
		diag.ReportPrevRun(context.Background(), prev)
	case prev.OtherAlive:
		log.Printf("WARNING: another oa-agent process (pid %d) holds %s/run.lock", prev.Info.PID, dir)
	}
	diag.SetRunLockRelease(release)
	return release
}

// registerEnvSecrets makes the exact secret values from the environment unprintable in reports, crash files and the
// diagnostic bundle even when they appear in free text.
func registerEnvSecrets() {
	for _, k := range []string{"OA_WORKER_TOKEN", "OA_SECRET_KEY", "OA_LLM_API_KEY"} {
		diag.RegisterSecret(os.Getenv(k))
	}
	if u, err := url.Parse(os.Getenv("OA_DATABASE_URL")); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			diag.RegisterSecret(pw)
		}
	}
}

// diagInfo is the process facts behind GET /v1/admin/system and the diagnostic bundle's effective configuration
// (no secrets: no tokens, keys or URLs with credentials).
func diagInfo(started time.Time, standalone bool, archiveRoot, addr, spool, workerURL string) api.DiagInfo {
	flag := func(k string) string { return os.Getenv(k) }
	host := func(raw string) string {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host
		}
		return ""
	}
	return api.DiagInfo{StartedAt: started, Standalone: standalone, ArchiveRoot: archiveRoot, LogDir: diag.LogDir(), Config: func() map[string]any {
		return map[string]any{
			"listen_addr": addr, "worker_host": host(workerURL), "spool_root": spool, "archive_root": archiveRoot, "log_dir": diag.LogDir(),
			"frontend_dir": flag("OA_FRONTEND_DIR"), "migrations_dir": flag("OA_MIGRATIONS_DIR"), "auto_migrate": flag("OA_AUTO_MIGRATE") == "1",
			"storage_enabled": flag("OA_STORAGE") != "off", "allow_reveal": flag("OA_ALLOW_REVEAL") != "0", "trusted_proxy": flag("OA_TRUSTED_PROXY") == "1",
			"public_url": strings.TrimRight(flag("OA_PUBLIC_URL"), "/"), "log_level": flag("OA_LOG_LEVEL"), "database": map[bool]string{true: "embedded or external (credentials not shown)", false: "not configured"}[flag("OA_DATABASE_URL") != ""],
			"build": buildinfo.String(),
		}
	}}
}
