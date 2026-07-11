package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationAdvisoryLockKey serializes migration runs across concurrently
// booting replicas: without it, two instances starting at once could both
// see a migration as unapplied and both execute it - one of this project's
// own migrations (005) drops and re-adds a constraint, which is not safe to
// run twice concurrently and could error out a booting replica.
const migrationAdvisoryLockKey = 869025551

// RunMigrations applies any pending SQL migration files in order.
// Each migration runs exactly once, tracked in the _migrations table.
// Safe for both fresh installs and existing databases.
func RunMigrations(conn *sql.DB) error {
	ctx := context.Background()

	// pg_advisory_lock is session-scoped, so acquire/release must happen on
	// the SAME physical connection - conn.Conn reserves one for the whole
	// migration run instead of letting the pool hand out a different
	// connection per Exec call (which would silently no-op the unlock).
	dedicated, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to reserve a connection for migrations: %w", err)
	}
	defer dedicated.Close()

	if _, err := dedicated.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("failed to acquire migration advisory lock: %w", err)
	}
	defer func() {
		if _, err := dedicated.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey); err != nil {
			log.Printf("[MIGRATION] failed to release advisory lock: %v", err)
		}
	}()

	createStmt := `CREATE TABLE IF NOT EXISTS _migrations (
		name VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := dedicated.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("failed to create _migrations tracking table: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		var applied bool
		err := dedicated.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM _migrations WHERE name = $1)", name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("failed to check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}

		sqlStr := strings.TrimSpace(string(data))
		if sqlStr == "" {
			continue
		}

		if err := applyMigrationTx(ctx, dedicated, name, sqlStr); err != nil {
			return err
		}

		log.Printf("[MIGRATION] Applied: %s", name)
	}

	return nil
}

// applyMigrationTx runs one migration file's SQL and its _migrations
// bookkeeping insert in a single transaction, so a failure partway through a
// multi-statement file can never leave it half-applied-but-unrecorded (which
// would otherwise re-run the surviving statements on the next boot and error
// on anything not idempotent).
func applyMigrationTx(ctx context.Context, conn *sql.Conn, name, sqlStr string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction for migration %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, sqlStr); err != nil {
		return fmt.Errorf("failed to apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO _migrations (name) VALUES ($1)", name); err != nil {
		return fmt.Errorf("failed to record migration %s: %w", name, err)
	}
	return tx.Commit()
}
