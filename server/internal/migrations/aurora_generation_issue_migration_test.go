package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const auroraIssueMigrationTestSchema = "aurora_issue_migration_test"

// TestAuroraGenerationIssueMigrationUpDown applies the issue_id column and its
// partial concurrent index in an isolated schema so the assertions cannot be
// satisfied by an artifact left in the shared database. The down migrations
// must remove both.
func TestAuroraGenerationIssueMigrationUpDown(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire Postgres connection: %v", err)
	}
	defer conn.Release()

	cleanup := func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+auroraIssueMigrationTestSchema+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+auroraIssueMigrationTestSchema); err != nil {
		t.Fatalf("create isolated migration schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, auroraIssueMigrationTestSchema); err != nil {
		t.Fatalf("set isolated migration search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE aurora_generation (id UUID PRIMARY KEY)`); err != nil {
		t.Fatalf("create pre-migration aurora_generation table: %v", err)
	}

	applyMigrationFile(t, ctx, conn.Conn(), "586_aurora_generation_issue.up.sql")
	applyMigrationFile(t, ctx, conn.Conn(), "587_aurora_generation_issue_idx.up.sql")

	var columnType string
	if err := conn.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = 'aurora_generation' AND a.attname = 'issue_id'
		), '')`,
		auroraIssueMigrationTestSchema,
	).Scan(&columnType); err != nil {
		t.Fatalf("read issue_id column type: %v", err)
	}
	if columnType != "uuid" {
		t.Fatalf("issue_id column type = %q, want uuid", columnType)
	}

	var indexExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = $1 AND indexname = 'aurora_generation_issue_idx')`,
		auroraIssueMigrationTestSchema,
	).Scan(&indexExists); err != nil {
		t.Fatalf("inspect aurora_generation_issue_idx: %v", err)
	}
	if !indexExists {
		t.Fatal("aurora_generation_issue_idx missing after up migration")
	}

	applyMigrationFile(t, ctx, conn.Conn(), "587_aurora_generation_issue_idx.down.sql")
	applyMigrationFile(t, ctx, conn.Conn(), "586_aurora_generation_issue.down.sql")

	if err := conn.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = 'aurora_generation' AND a.attname = 'issue_id'
		), '')`,
		auroraIssueMigrationTestSchema,
	).Scan(&columnType); err != nil {
		t.Fatalf("read issue_id column type after down: %v", err)
	}
	if columnType != "" {
		t.Fatalf("issue_id column still present after down migration (type %q)", columnType)
	}

	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = $1 AND indexname = 'aurora_generation_issue_idx')`,
		auroraIssueMigrationTestSchema,
	).Scan(&indexExists); err != nil {
		t.Fatalf("inspect aurora_generation_issue_idx after down: %v", err)
	}
	if indexExists {
		t.Fatal("aurora_generation_issue_idx still present after down migration")
	}
}
