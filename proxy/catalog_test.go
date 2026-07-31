package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/db"
	"gateway/pricing"
)

func TestCatalogRecordIDIsStableAndCompatibilitySensitive(t *testing.T) {
	model := &db.Model{
		Name:                         "minimax-m3",
		TargetModel:                  "minimax/minimax-m3",
		DisplayName:                  "MiniMax M3",
		Tags:                         []string{"muhiyacode"},
		ProviderFamily:               "minimax-openrouter",
		AdapterVersion:               "1",
		CompatibilityEpoch:           1,
		ContextWindow:                1_000_000,
		MaxOutputTokens:              16_000,
		SupportedParameters:          []string{"tools", "messages"},
		CacheContract:                `{"supports_prompt_cache":true}`,
		PricingRuleSetID:             "pricing:model-minimax-m3",
		InputCostNanoPerMillion:      300_000_000,
		OutputCostNanoPerMillion:     1_200_000_000,
		CacheReadCostNanoPerMillion:  60_000_000,
		CacheWriteCostNanoPerMillion: 300_000_000,
		PricingTiers: []pricing.Tier{{
			MinInputTokensExclusive: 512_000,
			Rates:                   pricing.Rates{InputPerMillion: 600_000_000},
		}},
		Health: "healthy",
	}
	first, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	model.Health = "degraded"
	model.DisplayName = "New label"
	second, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	if first.RecordID != second.RecordID {
		t.Fatal("mutable health/display fields must not change immutable record id")
	}
	model.CompatibilityEpoch++
	third, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	if third.RecordID == first.RecordID {
		t.Fatal("compatibility epoch must change immutable record id")
	}
}

func TestCatalogRejectsMalformedCacheContract(t *testing.T) {
	_, err := catalogRecord(&db.Model{Name: "x", CacheContract: "{"})
	if err == nil {
		t.Fatal("malformed cache contract unexpectedly accepted")
	}
}

// TestCatalogRecordIDIgnoresPriceAndCapabilityEdits is the load-bearing
// regression for GW-4: before RecordID was narrowed to the wire-contract
// fields, an operator editing a model's PRICE (or its context window, max
// output tokens, or capability flags) while a session was live changed the
// RecordID, and the next request's X-Muhiya-Expected-Model-Record no longer
// matched — establishModelResolution returned an unrecoverable 409 over a
// change with zero wire impact.
func TestCatalogRecordIDIgnoresPriceAndCapabilityEdits(t *testing.T) {
	model := &db.Model{
		Name:                     "deepseek-chat",
		TargetModel:              "deepseek-chat",
		ProviderFamily:           "deepseek",
		CompatibilityEpoch:       1,
		SupportedParameters:      []string{"tools", "messages"},
		CacheContract:            `{"supports_prompt_cache":true}`,
		ContextWindow:            64_000,
		MaxOutputTokens:          8_192,
		InputCostNanoPerMillion:  140_000,
		OutputCostNanoPerMillion: 280_000,
		SupportsVision:           false,
	}
	before, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an admin-panel price change, a context-window correction, and
	// enabling a capability flag — none of these alter what bytes the gateway
	// forwards or how it interprets them.
	model.InputCostNanoPerMillion = 999_000_000
	model.OutputCostNanoPerMillion = 999_000_000
	model.ContextWindow = 128_000
	model.MaxOutputTokens = 16_384
	model.SupportsVision = true
	after, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	if before.RecordID != after.RecordID {
		t.Fatalf("RecordID changed on a price/capability-only edit: %s -> %s", before.RecordID, after.RecordID)
	}
}

// TestExplainReportsExclusionReason is the GW-5 diagnostic: an operator
// asking "why isn't my model showing up in MuhiyaCode?" previously had no
// way to find out from the API itself.
func TestExplainReportsExclusionReason(t *testing.T) {
	cases := []struct {
		name   string
		model  db.Model
		wantIn bool
		reason string
	}{
		{"router model", db.Model{Name: routerModelDeprecated, Status: "active", MuhiyaCodeVisible: true}, false, "deprecated router model"},
		{"inactive status", db.Model{Name: "m1", Status: "inactive", MuhiyaCodeVisible: true}, false, "status=inactive"},
		{"transcribe-only", db.Model{Name: "m2", Status: "active", Transcribe: true, MuhiyaCodeVisible: true}, false, "transcribe-only"},
		{"not visible", db.Model{Name: "m3", Status: "active", MuhiyaCodeVisible: false}, false, "not muhiyacode_visible"},
		{"fully discoverable", db.Model{Name: "m4", Status: "active", MuhiyaCodeVisible: true}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := explainMuhiyaCodeDiscoverability(&c.model)
			if got.Included != c.wantIn {
				t.Fatalf("Included = %v, want %v", got.Included, c.wantIn)
			}
			if got.ExcludedReason != c.reason {
				t.Fatalf("ExcludedReason = %q, want %q", got.ExcludedReason, c.reason)
			}
			if got.ModelID != c.model.Name {
				t.Fatalf("ModelID = %q, want %q", got.ModelID, c.model.Name)
			}
		})
	}
}

// TestModelRecordMismatchSetsRefreshCatalogHeader confirms establishModelResolution
// signals X-Muhiya-Refresh-Catalog on a 409 so a client that CAN re-discover the
// catalog knows to retry once with a fresh record id rather than abandon the task.
func TestModelRecordMismatchSetsRefreshCatalogHeader(t *testing.T) {
	h := &ProxyHandler{}
	model := &db.Model{Name: "deepseek-chat", TargetModel: "deepseek-chat", CompatibilityEpoch: 1}
	record, err := catalogRecord(model)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Muhiya-Expected-Model-Record", record.RecordID+"-stale")
	w := httptest.NewRecorder()
	if ok := h.establishModelResolution(w, r, model); ok {
		t.Fatal("a mismatched expected record must be rejected")
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if w.Header().Get("X-Muhiya-Refresh-Catalog") != "true" {
		t.Fatal("expected X-Muhiya-Refresh-Catalog: true on a record mismatch")
	}
}
