package db

import (
	"strings"
	"testing"
)

// These tests need no database: they validate the embedded migration set so a
// missing/renamed file or a reverted data-loss fix fails CI instead of prod.

func TestMigrationsEmbedded(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
			data, err := migrationFS.ReadFile("migrations/" + e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			if len(strings.TrimSpace(string(data))) == 0 {
				t.Fatalf("migration %s is empty", e.Name())
			}
		}
	}
	for _, required := range []string{
		"001_initial.sql",
		"002_add_model_info_fields.sql",
		"003_add_indexes.sql",
		"004_add_thinking_level.sql",
		"005_preserve_request_logs.sql",
		"009_minimax_models.sql",
		"015_openrouter_provider.sql",
		"016_model_supports_vision.sql",
		"017_activate_gemma_vision.sql",
		"018_model_supports_thinking.sql",
		"019_default_transcription_language.sql",
		"020_model_media_capabilities.sql",
		"021_model_muhiyacode_visible.sql",
	} {
		found := false
		for _, n := range names {
			if n == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("required migration %s missing from embed (have %v)", required, names)
		}
	}
}

func TestMiniMaxMigrationIsAdditiveAndComplete(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/009_minimax_models.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, required := range []string{
		"https://api.minimax.io/v1", "minimax-m3", "minimax-m2.7",
		"minimax-m2.7-highspeed", "minimax-m2.5", "minimax-m2.1", "minimax-m2",
		"1000000", "204800", "on conflict (id) do nothing",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("MiniMax migration missing %q", required)
		}
	}
	for _, destructive := range []string{"drop table", "drop column", "delete from"} {
		if strings.Contains(sql, destructive) {
			t.Fatalf("MiniMax migration contains destructive statement %q", destructive)
		}
	}
}

// The 005 migration is the guard against a single user-delete cascade-wiping
// all request-log history. If someone re-introduces CASCADE here, prod audit
// data becomes deletable again - fail loudly.
func TestRequestLogHistoryIsPreserved(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/005_preserve_request_logs.sql")
	if err != nil {
		t.Fatalf("read 005: %v", err)
	}
	// Inspect only executable DDL, not the "-- ..." comments (which legitimately
	// describe the old CASCADE behavior being replaced).
	var ddlLines []string
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "--") {
			ddlLines = append(ddlLines, line)
		}
	}
	sql := strings.ToLower(strings.Join(ddlLines, "\n"))
	for _, needle := range []string{
		"request_logs_virtual_key_id_fkey",
		"request_logs_user_id_fkey",
		"on delete set null",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("005 migration must contain %q to preserve log history", needle)
		}
	}
	if strings.Contains(sql, "on delete cascade") {
		t.Fatal("005 migration must not re-introduce ON DELETE CASCADE on request_logs")
	}
}
