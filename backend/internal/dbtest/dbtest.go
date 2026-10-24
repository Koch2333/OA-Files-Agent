// Package dbtest gives integration tests an isolated, fully migrated store on a real server (see package dbiso for
// how the engine is selected and how isolation works).
package dbtest

import (
	"testing"

	"oa-agent/backend/internal/dbtest/dbiso"
	"oa-agent/backend/internal/store"
)

// New returns a store on an isolated, migrated database (skips when none is configured).
func New(t *testing.T) *store.Store { return &store.Store{Pool: dbiso.NewDB(t)} }
