package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/buildinfo"
	_ "oa-agent/backend/internal/cloud/all"
	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/dbadmin"
	"oa-agent/backend/internal/dbconfig"
	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/fileops"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/llm"
	"oa-agent/backend/internal/logx"
	"oa-agent/backend/internal/pipeline"
	"oa-agent/backend/internal/preview"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/sqldb"
	"oa-agent/backend/internal/storage"
	"oa-agent/backend/internal/store"
	"oa-agent/backend/internal/supervisor"
	"oa-agent/backend/internal/worker"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		diag.Fatal(err)
	}
	return a
}

func main() {
	defer diag.RecoverMain() // a panic on the main goroutine leaves a crash file and a fatal report
	started := time.Now()
	log.Printf("oa-agent %s", buildinfo.String())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Standalone (single-machine Windows install): helper processes live next to the executable.
	var layout supervisor.Layout
	standalone := os.Getenv("OA_STANDALONE") == "1"
	if standalone {
		exe, err := os.Executable()
		if err != nil {
			diag.Fatal(err)
		}
		root := env("OA_HOME", filepath.Dir(exe))
		if err := supervisor.LoadEnvFile(filepath.Join(root, "oa-agent.env")); err != nil {
			diag.Fatalf("oa-agent.env: %v", err)
		}
		layout = supervisor.NewLayout(root)
		supervisor.TieChildrenToProcess() // helpers die with this process, even on a hard kill
		if err := os.MkdirAll(layout.Logs, 0o700); err == nil {
			if lf, err := os.OpenFile(filepath.Join(layout.Logs, "oa-agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				defer lf.Close()
				log.SetOutput(io.MultiWriter(os.Stderr, lf))
			}
		}
		os.Setenv("OA_SPOOL_ROOT", env("OA_SPOOL_ROOT", layout.Spool))
		os.Setenv("OA_ARCHIVE_ROOT", env("OA_ARCHIVE_ROOT", layout.Archive))
		os.Setenv("OA_FRONTEND_DIR", env("OA_FRONTEND_DIR", layout.Web))
		os.Setenv("OA_MIGRATIONS_DIR", env("OA_MIGRATIONS_DIR", layout.Migrations))
		os.Setenv("OA_AUTO_MIGRATE", "1")
		if os.Getenv("OA_WORKER_URL") == "" {
			log.Print("starting document worker …")
			w, err := supervisor.StartWorker(ctx, layout)
			if err != nil {
				diag.Fatalf("document worker: %v", err)
			}
			defer w.Stop()
			os.Setenv("OA_WORKER_URL", w.URL)
			os.Setenv("OA_WORKER_TOKEN", w.Token)
		}
	}

	addr := env("OA_LISTEN_ADDR", "127.0.0.1:8080")
	workerURL := env("OA_WORKER_URL", "http://127.0.0.1:8090")
	workerToken := os.Getenv("OA_WORKER_TOKEN")
	spool := abs(env("OA_SPOOL_ROOT", "./data/spool"))
	archiveRoot := abs(env("OA_ARCHIVE_ROOT", "./data/archive"))
	if !standalone {
		defer initDiag(env("OA_LOG_DIR", filepath.Join(filepath.Dir(spool), "logs")))()
	}

	workerClient, err := worker.New(workerURL, workerToken)
	if err != nil {
		diag.Fatal(err)
	}
	// Which database? Environment > settings file (admin API) > embedded PostgreSQL (standalone only).
	dataDir := filepath.Dir(spool)
	box, err := secrets.Open(os.Getenv("OA_SECRET_KEY"), filepath.Join(dataDir, "secret.key"))
	if err != nil {
		diag.Fatalf("secret key: %v", err)
	}
	migrationsDir := env("OA_MIGRATIONS_DIR", "../migrations")
	resolved, err := dbconfig.Resolve(os.Getenv, dataDir, box)
	if err != nil {
		diag.Fatalf("database settings: %v", err)
	}
	if resolved.Source != dbconfig.SourceEnv && dbadmin.Promote(ctx, dataDir, migrationsDir, box, log.Printf) {
		if resolved, err = dbconfig.Resolve(os.Getenv, dataDir, box); err != nil {
			diag.Fatalf("database settings: %v", err)
		}
	}
	if !resolved.Configured() && standalone {
		// Only the standalone install falls back to its embedded PostgreSQL; with an external database configured
		// no embedded database is started at all.
		log.Print("starting embedded PostgreSQL …")
		pg, err := supervisor.StartPostgres(ctx, layout)
		if err != nil {
			diag.Fatalf("embedded PostgreSQL: %v", err)
		}
		defer pg.Stop()
		ec, err := sqldb.FromURL(pg.URL)
		if err != nil {
			diag.Fatalf("embedded PostgreSQL: %v", err)
		}
		resolved = dbconfig.Resolved{Config: ec.Normalize(), Source: dbconfig.SourceEmbedded}
	}
	var dbPool *sqldb.DB
	deps := api.Deps{Checks: []api.DependencyCheck{func(ctx context.Context) string { return db.Check(ctx, dbPool) }, workerClient.Check}}

	// Cross-origin browser access (e.g. the Vite dev server) is off unless listed; debug builds allow the Vite origins.
	corsList := strings.Split(os.Getenv("OA_CORS_ORIGINS"), ",")
	if os.Getenv("OA_CORS_ORIGINS") == "" && buildinfo.Debug {
		corsList = []string{"http://localhost:5173", "http://127.0.0.1:5173"}
	}
	if deps.CORSOrigins, err = api.NormalizeOrigins(corsList); err != nil {
		diag.Fatalf("OA_CORS_ORIGINS: %v", err)
	}

	if resolved.Configured() {
		if workerToken == "" {
			diag.Fatal("OA_WORKER_TOKEN is required when a database is configured")
		}
		st, err := store.OpenConfig(ctx, resolved.Config)
		if err != nil {
			diag.Fatalf("database (%s): %v", resolved.Config.Provider.Label(), err)
		}
		defer st.Close()
		dbPool = st.Pool
		log.Printf("database: %s (%s), source=%s", resolved.Config.Provider.Label(), sqldb.RedactDSN(resolved.Config.Address()), resolved.Source)
		rep := dbadmin.Inspect(ctx, st.Pool)
		if os.Getenv("OA_AUTO_MIGRATE") == "1" || (standalone && resolved.Source == dbconfig.SourceEmbedded) {
			// OA_AUTO_MIGRATE=1 is the deployment-level approval to initialise / upgrade the schema. Isolation and the
			// server requirements are enforced first: nothing is created in a shared schema or on an unsupported server.
			if rep.Schema.State != "ok" && !rep.OK {
				diag.Fatalf("database is not usable for OA Agent: %s", (&dbadmin.ValidationError{Report: rep}).Error())
			}
			if err := db.Apply(ctx, st.Pool, db.Options{Dir: migrationsDir, Logf: log.Printf}); err != nil {
				diag.Fatalf("database migration failed (nothing was partially applied on PostgreSQL/SQL Server; MySQL resumes at the failed step): %v", err)
			}
			log.Print("database migrations applied")
			rep = dbadmin.Inspect(ctx, st.Pool)
		}
		for _, c := range rep.Checks {
			if c.Status == dbadmin.Warn || c.Status == dbadmin.Fail {
				log.Printf("database check [%s] %s: %s", c.Status, c.Title, c.Detail)
			}
		}
		registerEnvSecrets()
		diag.Attach(st.Pool) // error reports persist from here on (earlier ones were queued in memory)
		if db.Check(ctx, st.Pool) != "ok" {
			diag.Fatal("database schema is not initialised or not up to date: approve the initialisation in Settings > Database, run `migrate` (cmd/migrate), or set OA_AUTO_MIGRATE=1")
		}
		ext, err := harness.NewWorkerExtractor(workerURL, workerToken)
		if err != nil {
			diag.Fatal(err)
		}
		if p := os.Getenv("OA_LLM_PRESETS_FILE"); p != "" {
			if err := llm.LoadOverride(p); err != nil {
				log.Printf("OA_LLM_PRESETS_FILE ignored (built-in presets stay active): %v", err)
			} else {
				log.Printf("model presets loaded from %s", p)
			}
		}
		// OA_LLM_* only seed the very first configuration; afterwards Settings (stored in the database) rule.
		seed := llm.Seed{Provider: os.Getenv("OA_LLM_PROVIDER"), BaseURL: os.Getenv("OA_LLM_BASE_URL"), Model: os.Getenv("OA_LLM_MODEL"), APIKey: os.Getenv("OA_LLM_API_KEY")}
		if n, err := strconv.Atoi(os.Getenv("OA_LLM_TIMEOUT_SECONDS")); err == nil && n > 0 {
			seed.Timeout = time.Duration(n) * time.Second
		}
		llmMgr, err := llm.New(ctx, st, box, seed)
		if err != nil {
			diag.Fatalf("recognition engine settings: %v", err)
		}
		// Test/mirror hook: point the OpenRouter login at another endpoint pair (https, or loopback for a fake provider).
		if a, x := os.Getenv("OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL"), os.Getenv("OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL"); a != "" || x != "" {
			if err := llmMgr.SetOAuthEndpoints("openrouter", a, x); err != nil {
				log.Printf("OA_LLM_OAUTH_OPENROUTER_* ignored: %v", err)
			}
		}
		agent := harness.NewAgent(llmMgr.Provider(), ext, harness.Options{
			CheckDuplicates: func(ctx context.Context, f harness.FileInput) ([]harness.DuplicateRef, error) { return nil, nil },
			// Page images are sent to a vision model only while the user has opted in (Settings → 视觉识别).
			Vision: func() harness.VisionTranscriber {
				if t := llmMgr.Transcriber(); t != nil {
					return t
				}
				return nil
			},
		})
		svc, err := pipeline.New(pipeline.Config{Store: st, Analyzer: agent, ArchiveRoot: archiveRoot, SpoolRoot: spool, ProviderInfo: llmMgr.Status, Models: llmMgr})
		if err != nil {
			diag.Fatal(err)
		}
		diag.Go("pipeline.run", func() { svc.Run(ctx) })
		deps.Store = st
		deps.DBAdmin = &dbadmin.Service{DataDir: dataDir, MigrationsDir: migrationsDir, Box: box, Resolved: resolved, Store: st,
			Audit: func(ctx context.Context, actor, action, target, summary string) {
				st.Audit(ctx, actor, action, target, summary)
			}}
		deps.Backend = svc
		if dir := os.Getenv("OA_FRONTEND_DIR"); dir != "" {
			deps.StaticDir = abs(dir)
		}
		deps.LLM = llmMgr
		llmMgr.Audit = func(ctx context.Context, actor, action, target, summary string) {
			st.Audit(ctx, actor, action, target, summary)
		}
		importSvc := &imports.Service{Store: st, Root: spool, Limits: imports.LimitsFromEnv(os.Getenv)}
		fsm := &fsman.Manager{Root: archiveRoot}
		if os.Getenv("OA_STORAGE") != "off" {
			sto := storage.New(storage.Config{Store: st, Box: box, Imports: importSvc, Archive: fsm})
			diag.Go("storage.run", func() { sto.Run(ctx) })
			deps.Storage = sto
		}
		authSvc := auth.New(st, box)
		authSvc.PublicURL = strings.TrimRight(strings.TrimSpace(os.Getenv("OA_PUBLIC_URL")), "/")
		authSvc.Logf = log.Printf
		deps.Auth = authSvc
		deps.TrustedProxy = os.Getenv("OA_TRUSTED_PROXY") == "1"
		deps.PublicURL = authSvc.PublicURL
		announceSetup(ctx, authSvc, addr)
		diag.Go("api.purge", func() {
			diag.Loop(ctx, "api.purge", func(ctx context.Context) { purgeAuth(ctx, st, authSvc) })
		})
		deps.Preview = &preview.Service{Store: st, FS: fsm, Worker: workerURL, Token: workerToken}
		deps.Files = &fileops.Service{Store: st, FS: fsm, AllowReveal: os.Getenv("OA_ALLOW_REVEAL") != "0"}
		if host, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
				deps.StrictLocal = true
			}
		}
		deps.Imports = importSvc
		diag.Go("imports.gc", func() { importSvc.StartGC(ctx) })
		if l := importSvc.Limits; l.MaxUploadBytes > 0 || l.MaxBatchFiles > 0 {
			log.Printf("import safety valves active: OA_MAX_UPLOAD_BYTES=%d OA_MAX_BATCH_FILES=%d (0 = unlimited)", l.MaxUploadBytes, l.MaxBatchFiles)
		}
		log.Printf("business API enabled (engine=%s, spool=%s, archive=%s)", llmMgr.Config().Mode, spool, archiveRoot)
		logx.Debugf("debug build: listen=%s worker=%s storage=%v frontend=%q", addr, workerURL, deps.Storage != nil, deps.StaticDir)
	} else {
		if dir := os.Getenv("OA_FRONTEND_DIR"); dir != "" {
			deps.StaticDir = abs(dir)
		}
		log.Print("no database configured (OA_DB_PROVIDER / OA_DATABASE_URL): only health/docs endpoints are available")
	}

	deps.Diag = diagInfo(started, standalone, archiveRoot, addr, spool, workerURL)
	srv := &http.Server{Addr: addr, Handler: api.NewServer(deps), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second} // upload handlers extend their own read/write deadlines per read (api/uploads.go), so large uploads are never cut by these
	diag.Go("api.shutdown", func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	})
	log.Printf("OA API listening on %s", addr)
	diag.MarkRunning()
	if standalone && os.Getenv("OA_OPEN_BROWSER") != "0" {
		diag.Go("api.open_browser", func() { openBrowser("http://" + addr) })
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		diag.Fatal(err)
	}
}
