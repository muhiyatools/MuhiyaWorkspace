package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Migrations run at boot, so a missing column normally cannot happen. It
// happens anyway when a migration is applied out of band, when a deploy points
// at a database an older build already migrated, or when a migration errored
// and the process kept running. The symptom is then an opaque 500 on the first
// write that touches the new column — far from the cause, and hours of log
// reading away from it.
//
// verifySchemaExpectations closes that gap by asserting at startup that the
// objects this build writes to actually exist, naming the migration that
// supplies anything missing.

// schemaExpectation is one object this build requires and the migration that
// creates it.
type schemaExpectation struct {
	table     string
	column    string // empty means "the table itself must exist"
	migration string
	purpose   string
}

// requiredSchema lists what the write paths depend on. It is deliberately
// short: only objects whose absence produces a confusing runtime failure rather
// than an obvious one, i.e. the ones added after the initial schema.
var requiredSchema = []schemaExpectation{
	{table: "models", column: "muhiyacode_visible", migration: "021_model_muhiyacode_visible.sql",
		purpose: "MuhiyaCode model discovery"},
	{table: "models", column: "muhiyachat_visible", migration: "038_model_muhiyachat_visible.sql",
		purpose: "MuhiyaChat model discovery"},
	// The request-log INSERT names these columns unconditionally, so their
	// absence fails every log write rather than degrading — worth naming
	// explicitly instead of surfacing as an opaque insert error.
	{table: "request_logs", column: "reasoning_tokens", migration: "039_request_latency_instrumentation.sql",
		purpose: "thinking-model latency attribution"},
	{table: "request_logs", column: "first_token_ms", migration: "039_request_latency_instrumentation.sql",
		purpose: "prefill vs generation latency split"},
	{table: "models", column: "prompt_accounting", migration: "032_prompt_accounting.sql",
		purpose: "per-provider prompt-token accounting (billing correctness)"},
	{table: "models", column: "coding_tier", migration: "041_model_spec_scores.sql",
		purpose: "model picker Intelligence rank"},
	{table: "models", column: "speed_score", migration: "041_model_spec_scores.sql",
		purpose: "model picker Speed rank"},
	{table: "model_pricing_tiers", migration: "025_model_pricing_tiers.sql",
		purpose: "long-context pricing tiers"},
	{table: "model_cache_ttl_rates", migration: "033_model_cache_ttl_rates.sql",
		purpose: "cache rates by entry lifetime"},
	{table: "model_price_windows", migration: "034_model_price_windows.sql",
		purpose: "time-of-day price windows"},
	{table: "request_pricing_lines", migration: "035_request_pricing_audit.sql",
		purpose: "per-request charge derivation"},
	{table: "request_logs", column: "pricing_rule_set_id", migration: "035_request_pricing_audit.sql",
		purpose: "pricing provenance on request logs"},
	{table: "request_logs", column: "usage_anomaly", migration: "032_prompt_accounting.sql",
		purpose: "inconsistent-usage reporting"},
}

// VerifySchemaExpectations returns an error naming every missing object and the
// migration that supplies it.
func VerifySchemaExpectations(conn *sql.DB) error {
	var problems []string
	for _, want := range requiredSchema {
		present, err := schemaObjectExists(conn, want)
		if err != nil {
			return fmt.Errorf("failed to inspect schema for %s: %w", want.describe(), err)
		}
		if !present {
			problems = append(problems, fmt.Sprintf("  - %s is missing (%s); supplied by db/migrations/%s",
				want.describe(), want.purpose, want.migration))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the database is missing objects this build requires:\n%s\n"+
			"Migrations run automatically at startup, so this usually means a migration "+
			"failed earlier or the database was migrated by a different build. Check the "+
			"_migrations table and the boot logs above",
		strings.Join(problems, "\n"))
}

func (s schemaExpectation) describe() string {
	if s.column == "" {
		return "table " + s.table
	}
	return "column " + s.table + "." + s.column
}

func schemaObjectExists(conn *sql.DB, want schemaExpectation) (bool, error) {
	var exists bool
	if want.column == "" {
		err := conn.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1)`, want.table).Scan(&exists)
		return exists, err
	}
	err := conn.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2)`,
		want.table, want.column).Scan(&exists)
	return exists, err
}
