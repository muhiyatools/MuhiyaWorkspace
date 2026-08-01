package db

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gateway/money"
	"gateway/pricing"
)

// A mismatched placeholder count in a hand-written INSERT/UPDATE compiles fine,
// passes vet, and fails only at runtime as an opaque 500 from the admin API.
// These tests read the actual SQL out of db.go and check it adds up, so the
// model write path cannot drift out of shape unnoticed.

func modelWriteSQL(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

var placeholderPattern = regexp.MustCompile(`\$(\d+)`)

// highestPlaceholder returns the largest $N in a statement.
func highestPlaceholder(statement string) int {
	highest := 0
	for _, match := range placeholderPattern.FindAllStringSubmatch(statement, -1) {
		if n, err := strconv.Atoi(match[1]); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// TestModelInsertsAreBalanced checks EVERY models INSERT — there are two (the
// admin CreateModel path and seedDefaults), and an earlier version of this test
// silently validated only the first one it found, which is the same
// false-confidence trap it exists to prevent.
func TestModelInsertsAreBalanced(t *testing.T) {
	source := modelWriteSQL(t)
	const marker = "INSERT INTO models ("

	found := 0
	for offset := 0; ; {
		index := strings.Index(source[offset:], marker)
		if index < 0 {
			break
		}
		start := offset + index
		offset = start + len(marker)

		columnsEnd := strings.Index(source[start:], ") VALUES (")
		if columnsEnd < 0 {
			t.Fatalf("INSERT at offset %d has no VALUES clause", start)
		}
		columnBlock := source[start+len(marker) : start+columnsEnd]
		columns := 0
		for _, part := range strings.Split(columnBlock, ",") {
			if strings.TrimSpace(part) != "" {
				columns++
			}
		}

		valuesStart := start + columnsEnd
		valuesEnd := strings.Index(source[valuesStart:], "`,")
		if valuesEnd < 0 {
			t.Fatalf("INSERT at offset %d has no terminating backtick", start)
		}
		values := highestPlaceholder(source[valuesStart : valuesStart+valuesEnd])

		if columns == 0 || values == 0 {
			t.Fatalf("INSERT at offset %d parsed as %d columns / %d placeholders; "+
				"the test's extraction is broken, not the query", start, columns, values)
		}
		if columns != values {
			t.Errorf("models INSERT at offset %d lists %d columns but %d placeholders", start, columns, values)
		}
		t.Logf("models INSERT at offset %d: %d columns, %d placeholders", start, columns, values)
		found++
	}
	if found < 2 {
		t.Fatalf("found only %d models INSERT statements; expected the CreateModel and seedDefaults paths", found)
	}
}

// TestUpdateModelPlaceholdersAreContiguous checks the UPDATE assigns each
// placeholder exactly once and that the WHERE clause uses the last one.
func TestUpdateModelPlaceholdersAreContiguous(t *testing.T) {
	source := modelWriteSQL(t)
	start := strings.Index(source, "UPDATE models SET")
	if start < 0 {
		t.Fatal("could not find the models UPDATE")
	}
	end := strings.Index(source[start:], "`,")
	if end < 0 {
		t.Fatal("could not find the end of the UPDATE statement")
	}
	statement := source[start : start+end]

	seen := map[int]int{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(statement, -1) {
		n, _ := strconv.Atoi(match[1])
		seen[n]++
	}
	highest := highestPlaceholder(statement)
	if highest == 0 {
		t.Fatal("no placeholders found; the test's SQL extraction is broken")
	}
	for i := 1; i <= highest; i++ {
		switch seen[i] {
		case 1:
		case 0:
			t.Errorf("UPDATE skips placeholder $%d — arguments after it bind to the wrong column", i)
		default:
			t.Errorf("UPDATE uses placeholder $%d %d times", i, seen[i])
		}
	}
	if !strings.Contains(statement, "WHERE id = $"+strconv.Itoa(highest)) {
		t.Errorf("UPDATE's WHERE clause does not use the final placeholder $%d", highest)
	}
	t.Logf("models UPDATE: %d placeholders, WHERE uses $%d", highest, highest)
}

// Every column the write path names must be one the schema check knows about
// or one that predates it — this is what makes a missing migration a named
// boot failure instead of a runtime 500.
func TestSchemaExpectationsCoverTheNewPricingObjects(t *testing.T) {
	required := map[string]bool{}
	for _, want := range requiredSchema {
		required[want.describe()] = true
	}
	for _, want := range []string{
		"column models.prompt_accounting",
		"table model_cache_ttl_rates",
		"table model_price_windows",
		"table model_pricing_tiers",
		"table request_pricing_lines",
	} {
		if !required[want] {
			t.Errorf("requiredSchema does not cover %s; a missing migration would surface as an opaque 500", want)
		}
	}
}

func TestSchemaExpectationsNameARealMigration(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	for _, want := range requiredSchema {
		if !present[want.migration] {
			t.Errorf("%s points at migration %q, which is not in the embedded set",
				want.describe(), want.migration)
		}
		if strings.TrimSpace(want.purpose) == "" {
			t.Errorf("%s has no stated purpose", want.describe())
		}
	}
}

// Validation failures must stay ErrInvalidModelConfig so the admin API answers
// 400 with the reason. Anything unwrapped becomes a 500 the operator cannot act
// on — which is exactly the failure mode this whole file exists to prevent.
func TestModelValidationFailuresAreClassifiedNotOpaque(t *testing.T) {
	rate := money.NanoUSD(1_000)
	cases := map[string]Model{
		"unknown cache TTL": {
			ID: "m", Name: "m", PricingRuleSetID: "pricing:m",
			CacheTTLRates: []pricing.TTLRate{{TTL: "30m", CacheWritePerMillion: &rate}},
		},
		"ambiguous price window": {
			ID: "m", Name: "m", PricingRuleSetID: "pricing:m",
			PriceWindows: []pricing.Window{{
				Label: "bad", StartMinuteUTC: 600, EndMinuteUTC: 600,
				MultiplierNum: 2, MultiplierDen: 1,
			}},
		},
		"negative tier rate": {
			ID: "m", Name: "m", PricingRuleSetID: "pricing:m",
			PricingTiers: []pricing.Tier{{
				MinInputTokensExclusive: 1000,
				Rates:                   pricing.Rates{InputPerMillion: -1},
			}},
		},
	}
	for name, model := range cases {
		err := validateModelPricingRules(model)
		if err == nil {
			t.Errorf("%s: expected rejection", name)
			continue
		}
		if !errors.Is(err, ErrInvalidModelConfig) {
			t.Errorf("%s: error is not ErrInvalidModelConfig, so the admin API answers 500 instead of 400: %v",
				name, err)
		}
	}
}

// A model saved with no pricing extras must validate: the common case is a
// model with none of these configured, and rejecting it would break every save.
func TestPlainModelStillValidates(t *testing.T) {
	model := Model{
		ID: "m", Name: "m", PricingRuleSetID: "pricing:m",
		InputCostNanoPerMillion:  1_000_000_000,
		OutputCostNanoPerMillion: 2_000_000_000,
		PromptAccounting:         string(pricing.PromptInclusive),
	}
	if err := validateModelPricingRules(model); err != nil {
		t.Fatalf("a plain model was rejected: %v", err)
	}
}
