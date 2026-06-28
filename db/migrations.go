package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migrationEntry struct {
	Name string
	SQL  string
}

// RunMigrations applies any pending SQL migration files in order.
// Each migration runs exactly once, tracked in the _migrations table.
// Safe for both fresh installs and existing databases.
func RunMigrations(conn *sql.DB) error {
	createStmt := `CREATE TABLE IF NOT EXISTS _migrations (
		name VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := conn.Exec(createStmt); err != nil {
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
		err := conn.QueryRow("SELECT EXISTS(SELECT 1 FROM _migrations WHERE name = $1)", name).Scan(&applied)
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

		if _, err := conn.Exec(sqlStr); err != nil {
			return fmt.Errorf("failed to apply migration %s: %w", name, err)
		}

		if _, err := conn.Exec("INSERT INTO _migrations (name) VALUES ($1)", name); err != nil {
			return fmt.Errorf("failed to record migration %s: %w", name, err)
		}

		log.Printf("[MIGRATION] Applied: %s", name)
	}

	return nil
}
