package db

import (
	"strings"
	"testing"

	"gateway/pricing"
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
		"023_exact_money_foundation.sql",
		"024_budget_reservation_link.sql",
		"025_model_pricing_tiers.sql",
		"026_catalog_v2_metadata.sql",
		"027_disable_automatic_model_routing.sql",
		"028_release_stranded_authorizations.sql",
		"029_remove_budget_reservations.sql",
		"030_request_logs_single_source.sql",
		"032_prompt_accounting.sql",
		"033_model_cache_ttl_rates.sql",
		"034_model_price_windows.sql",
		"035_request_pricing_audit.sql",
		"040_route_whisper_through_openrouter.sql",
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

func TestReservationRemovalMigrationDropsAllRuntimeState(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/029_remove_budget_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, required := range []string{
		"drop table if exists budget_reservations",
		"alter table request_logs drop column if exists reservation_id",
		"drop table if exists user_window_charge_state",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("reservation-removal migration missing %q", required)
		}
	}
}

func TestExactMoneyMigrationDefinesRequestLogAuthority(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/023_exact_money_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, required := range []string{
		"budget_nano_usd", "cost_nano_usd",
		"input_cost_nano_usd_per_million",
		"amount_nano_usd", "used_nano_usd",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("exact-money migration missing %q", required)
		}
	}
	if strings.Contains(sql, "account_ledger") {
		t.Fatal("exact-money migration must not create the retired account ledger")
	}
}

func TestRequestLogConsolidationMigrationPreservesThenDropsLedger(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/030_request_logs_single_source.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, required := range []string{
		"insert into request_logs",
		"from account_ledger",
		"drop table account_ledger",
		"session_id",
		"client_request_id",
		"attempt_number",
		"credits_consumed",
		"budget_window_id",
		"idx_request_logs_client_attempt_unique",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("request-log consolidation migration missing %q", required)
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

// TestPricingMigrationsCarryTheirLoadBearingStatements guards the pieces the
// pricing engine depends on at runtime. Without a database in CI these files
// are otherwise only checked for being non-empty, so a truncated or reverted
// migration would surface as a production column-not-found instead.
func TestPricingMigrationsCarryTheirLoadBearingStatements(t *testing.T) {
	cases := map[string][]string{
		"032_prompt_accounting.sql": {
			"add column if not exists prompt_accounting",
			"check (prompt_accounting in ('inclusive', 'exclusive'))",
			"set prompt_accounting = 'exclusive'",
			"add column if not exists usage_anomaly",
		},
		"033_model_cache_ttl_rates.sql": {
			"create table if not exists model_cache_ttl_rates",
			"check (ttl in ('5m', '1h', 'default'))",
		},
		"034_model_price_windows.sql": {
			"create table if not exists model_price_windows",
			"multiplier_den bigint not null default 1",
			"check (multiplier_den >  0)",
			"check (start_minute_utc <> end_minute_utc)",
		},
		"035_request_pricing_audit.sql": {
			"create table if not exists request_pricing_lines",
			"add column if not exists pricing_rule_set_id",
			"add column if not exists priced_at",
			"add column if not exists upstream_cost_nano_usd",
			"references request_logs(id) on delete cascade",
		},
	}
	for name, required := range cases {
		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := strings.ToLower(string(data))
		for _, statement := range required {
			if !strings.Contains(body, statement) {
				t.Errorf("migration %s is missing %q", name, statement)
			}
		}
	}
}

func TestWhisperOpenRouterMigrationKeepsVirtualModelContract(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/040_route_whisper_through_openrouter.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, required := range []string{
		"where name = 'whisper-1'",
		"provider_id = 'openrouter'",
		"target_model = 'openai/whisper-1'",
		"where id = 'openrouter'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("whisper migration missing %q", required)
		}
	}
}

// Every token class the pricing engine can emit must be accepted by the
// request_pricing_lines CHECK constraint, or a perfectly valid charge would
// fail to persist its own derivation.
func TestPricingLineClassesMatchTheEngine(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/035_request_pricing_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, class := range pricing.TokenClasses {
		if !strings.Contains(body, "'"+string(class)+"'") {
			t.Errorf("token class %q is not allowed by the request_pricing_lines constraint", class)
		}
	}
}
