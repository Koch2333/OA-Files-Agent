// Command migrate initialises or upgrades the OA Agent schema on PostgreSQL, MySQL or SQL Server.
//
// It is the operator-level approval step: nothing is changed unless -apply is given (the default is a dry run that
// prints the DDL). Connection: the same OA_DB_* / OA_DATABASE_URL environment as the server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/dbadmin"
	"oa-agent/backend/internal/dbconfig"
	"oa-agent/backend/internal/sqldb"
)

func main() {
	reset := flag.Bool("reset", false, "DEV ONLY: drop every OA Agent table of the target first (needs OA_ALLOW_RESET=1; PostgreSQL: drops and recreates the schema)")
	dir := flag.String("dir", "../migrations", "migrations root directory (PostgreSQL files in the root, others in mysql/ and mssql/)")
	apply := flag.Bool("apply", false, "apply the migrations (without it the DDL is only printed)")
	destructive := flag.Bool("allow-destructive", false, "allow migrations that contain destructive statements (explicit administrator approval)")
	skipIso := flag.Bool("skip-isolation-check", false, "do not refuse a target schema that contains foreign tables (NOT recommended)")
	flag.Parse()
	cfg, ok, err := dbconfig.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if !ok {
		log.Fatal("set OA_DB_PROVIDER (+ OA_DB_HOST ...) or OA_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d, err := sqldb.Open(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	rep := dbadmin.Inspect(ctx, d)
	for _, c := range rep.Checks {
		fmt.Printf("[%s] %s: %s\n", c.Status, c.Title, c.Detail)
	}
	if (!rep.OK || rep.Isolation == dbadmin.IsoNotIsolated) && !*skipIso {
		log.Fatal("the target database does not pass validation; fix the failed checks above")
	}
	if *reset {
		if os.Getenv("OA_ALLOW_RESET") != "1" {
			log.Fatal("-reset destroys all data; set OA_ALLOW_RESET=1 to confirm")
		}
		if err := dropAll(ctx, d); err != nil {
			log.Fatal(err)
		}
		log.Print("OA Agent tables dropped")
	}
	pl, err := db.MakePlan(ctx, d, *dir)
	if err != nil {
		log.Fatal(err)
	}
	if len(pl.Pending) == 0 {
		log.Print("database schema is up to date")
		return
	}
	for _, p := range pl.Pending {
		fmt.Printf("-- migration %s (destructive=%v resume=%v)\n", p.Name, p.Destructive, p.Resume)
		if !*apply {
			for _, s := range p.Steps {
				fmt.Println(s + ";")
			}
		}
	}
	if !*apply {
		log.Print("dry run only; re-run with -apply to execute")
		return
	}
	if err := db.Apply(ctx, d, db.Options{Dir: *dir, AllowDestructive: *destructive, Logf: log.Printf}); err != nil {
		log.Fatal(err)
	}
	log.Print("database migration complete")
}

func dropAll(ctx context.Context, d *sqldb.DB) error {
	if d.Provider() == sqldb.Postgres {
		_, err := d.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
		return err
	}
	tables, err := db.ListTables(ctx, d)
	if err != nil {
		return err
	}
	// drop in dependency order by repeating until nothing is left (foreign keys)
	left := tables
	for pass := 0; pass < 12 && len(left) > 0; pass++ {
		var next []string
		for _, t := range left {
			if !db.OATables[t] {
				continue
			}
			if _, err := d.Exec(ctx, "DROP TABLE "+d.Dialect().Quote(t)); err != nil {
				next = append(next, t)
			}
		}
		left = next
	}
	return nil
}
