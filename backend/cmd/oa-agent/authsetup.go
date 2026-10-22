package main

import (
	"context"
	"log"
	"strings"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/store"
)

// announceSetup prints a fresh one-time setup token at every start while the system is not initialised. Only the
// hash is stored; the plaintext exists in this log line and nowhere else (oa-admin setup-token issues a new one).
func announceSetup(ctx context.Context, svc *auth.Service, addr string) {
	tok, err := svc.EnsureSetupToken(ctx)
	if err != nil {
		diag.Fatalf("setup token: %v", err)
	}
	if tok == "" {
		return
	}
	log.Printf("SETUP TOKEN: %s", tok)
	log.Printf("the system is not initialised: open http://%s/setup and enter the setup token above (or run: oa-admin setup-token)", strings.Replace(addr, "0.0.0.0", "localhost", 1))
}

// purgeAuth removes expired sessions, challenges and old invitations periodically.
func purgeAuth(ctx context.Context, st *store.Store, svc *auth.Service) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		_ = st.PurgeExpiredAuth(ctx, svc.Now())
		// error reports: resolved groups older than 90 days, occurrences beyond 20 per group, at most 5000 groups
		if _, err := diag.Retention(ctx, st.Pool, time.Now()); err != nil && ctx.Err() == nil {
			log.Printf("error report retention: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
