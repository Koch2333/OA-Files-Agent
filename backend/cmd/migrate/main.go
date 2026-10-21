package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"oa-agent/backend/internal/db"
)

func main() {
	reset := flag.Bool("reset", false, "DEV ONLY: drop and recreate the public schema before migrating (needs OA_ALLOW_RESET=1)")
	dir := flag.String("dir", "../migrations", "directory containing NNN_*.up.sql migrations")
	flag.Parse()
	url := os.Getenv("OA_DATABASE_URL")
	if url == "" {
		log.Fatal("OA_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := db.Connect(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(context.Background())
	if *reset {
		if os.Getenv("OA_ALLOW_RESET") != "1" {
			log.Fatal("-reset destroys all data; set OA_ALLOW_RESET=1 to confirm")
		}
		if _, err := conn.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
			log.Fatal(err)
		}
		log.Print("public schema reset")
	}
	if err := db.MigrateDir(ctx, conn, *dir); err != nil {
		log.Fatal(err)
	}
	log.Print("database migration complete")
}
