package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewFleetFixture requires an explicitly selected, already migrated database.
// It never creates a database or falls back to a shared development database.
func NewFleetFixture(t *testing.T) (*pgxpool.Pool, *Fixture) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("Fleet tests require explicit DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid Fleet test database configuration")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "2000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("Fleet test database setup failed")
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("Fleet test database unavailable")
	}
	var database string
	if err = pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	t.Logf("Fleet fixture database=%s; test-owned rows only", database)
	f := New(pool, "", "")
	suffix := uuid.NewString()
	f.UserID = f.User(t, "fleet fixture", "fleet-"+suffix+"@test.invalid")
	f.WorkspaceID = f.Workspace(t, "fleet fixture", "fleet-"+suffix)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	return pool, f
}

// FleetProfile owns a private fake file by default; references never point at host credentials.
func (f *Fixture) FleetProfile(t TB, namespace string, over ...Cols) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "multica-fleet-profile-")
	if err != nil {
		t.Fatalf("setup: private profile directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ref := filepath.Join(dir, "profile.json")
	if err = os.WriteFile(ref, []byte(`{"api_key":"fake-fixture-marker"}`), 0600); err != nil {
		t.Fatalf("setup: private profile file: %v", err)
	}
	return f.Insert(t, "fleet_credential_profiles", merge(Cols{"namespace": namespace, "owner_id": f.UserID, "profile_ref": ref, "config_version": int64(1)}, over))
}

// FleetNode inserts a ready node without touching a provider or agent executable.
func (f *Fixture) FleetNode(t TB, namespace string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "fleet_nodes", merge(Cols{"namespace": namespace, "owner_id": f.UserID, "name": "fixture", "desired": "running", "status": "ready", "ready": true, "health_at": time.Now().UTC()}, over))
}
