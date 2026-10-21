package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/pipeline"
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
		log.Fatal(err)
	}
	return a
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Standalone (single-machine Windows install): helper processes live next to the executable.
	var layout supervisor.Layout
	standalone := os.Getenv("OA_STANDALONE") == "1"
	if standalone {
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		root := env("OA_HOME", filepath.Dir(exe))
		if err := supervisor.LoadEnvFile(filepath.Join(root, "oa-agent.env")); err != nil {
			log.Fatalf("oa-agent.env: %v", err)
		}
		layout = supervisor.NewLayout(root)
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
		if os.Getenv("OA_DATABASE_URL") == "" {
			log.Print("starting embedded PostgreSQL …")
			pg, err := supervisor.StartPostgres(ctx, layout)
			if err != nil {
				log.Fatalf("embedded PostgreSQL: %v", err)
			}
			defer pg.Stop()
			os.Setenv("OA_DATABASE_URL", pg.URL)
		}
		if os.Getenv("OA_WORKER_URL") == "" {
			log.Print("starting document worker …")
			w, err := supervisor.StartWorker(ctx, layout)
			if err != nil {
				log.Fatalf("document worker: %v", err)
			}
			defer w.Stop()
			os.Setenv("OA_WORKER_URL", w.URL)
			os.Setenv("OA_WORKER_TOKEN", w.Token)
		}
	}

	addr := env("OA_LISTEN_ADDR", "127.0.0.1:8080")
	databaseURL := os.Getenv("OA_DATABASE_URL")
	workerURL := env("OA_WORKER_URL", "http://127.0.0.1:8090")
	workerToken := os.Getenv("OA_WORKER_TOKEN")
	spool := abs(env("OA_SPOOL_ROOT", "./data/spool"))
	archiveRoot := abs(env("OA_ARCHIVE_ROOT", "./data/archive"))

	workerClient, err := worker.New(workerURL, workerToken)
	if err != nil {
		log.Fatal(err)
	}
	deps := api.Deps{Checks: []api.DependencyCheck{func(ctx context.Context) string { return db.Check(ctx, databaseURL) }, workerClient.Check}}

	if databaseURL != "" {
		if os.Getenv("OA_AUTO_MIGRATE") == "1" {
			conn, err := db.Connect(ctx, databaseURL)
			if err != nil {
				log.Fatal(err)
			}
			if err := db.MigrateDir(ctx, conn, env("OA_MIGRATIONS_DIR", "../migrations")); err != nil {
				log.Fatal(err)
			}
			conn.Close(context.Background())
			log.Print("database migrations applied")
		}
		if workerToken == "" {
			log.Fatal("OA_WORKER_TOKEN is required when a database is configured")
		}
		st, err := store.Open(ctx, databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		if db.Check(ctx, databaseURL) != "ok" {
			log.Fatal("database schema is not up to date: run `go run ./cmd/migrate` (or set OA_AUTO_MIGRATE=1)")
		}
		ext, err := harness.NewWorkerExtractor(workerURL, workerToken)
		if err != nil {
			log.Fatal(err)
		}
		var prov harness.Provider = harness.Heuristic{}
		name, model, providerOK := "heuristic", "rule-based（离线，非 LLM）", true
		if strings.EqualFold(os.Getenv("OA_LLM_PROVIDER"), "openai") {
			cfg := harness.OpenAIConfigFromEnv()
			p, err := harness.NewOpenAICompatible(cfg)
			if err != nil {
				log.Fatalf("LLM provider: %v", err)
			}
			prov, name, model, providerOK = p, "openai-compatible", cfg.Model, true
		}
		agent := harness.NewAgent(prov, ext, harness.Options{CheckDuplicates: func(ctx context.Context, f harness.FileInput) ([]harness.DuplicateRef, error) {
			return nil, nil
		}})
		svc, err := pipeline.New(pipeline.Config{Store: st, Analyzer: agent, ArchiveRoot: archiveRoot, SpoolRoot: spool, ProviderName: name, Model: model, ProviderOK: providerOK})
		if err != nil {
			log.Fatal(err)
		}
		go svc.Run(ctx)
		deps.Store = st
		deps.Backend = svc
		if dir := os.Getenv("OA_FRONTEND_DIR"); dir != "" {
			deps.StaticDir = abs(dir)
		}
		deps.Imports = &imports.Service{Store: st, Root: spool}
		log.Printf("business API enabled (provider=%s, spool=%s, archive=%s)", name, spool, archiveRoot)
	} else {
		if dir := os.Getenv("OA_FRONTEND_DIR"); dir != "" {
			deps.StaticDir = abs(dir)
		}
		log.Print("OA_DATABASE_URL not set: only health/docs endpoints are available")
	}

	srv := &http.Server{Addr: addr, Handler: api.NewServer(deps), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	log.Printf("OA API listening on %s", addr)
	if standalone && os.Getenv("OA_OPEN_BROWSER") != "0" {
		go openBrowser("http://" + addr)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
