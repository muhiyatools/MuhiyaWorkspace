package db

import (
	"errors"
	"strings"
	"testing"

	"gateway/money"
	"gateway/pricing"
)

func TestRequestLogFilterUsesParametersForAllUserInput(t *testing.T) {
	where, args := requestLogFilter(RequestLogQuery{
		UserID:  "user-1",
		KeyID:   "key-1",
		ModelID: "minimax-m3",
		Search:  "%'; DROP TABLE request_logs; --",
		Status:  "error",
	})

	if len(args) != 4 {
		t.Fatalf("got %d arguments, want 4", len(args))
	}
	for _, value := range args {
		if strings.Contains(where, value.(string)) {
			t.Fatalf("user-controlled value was interpolated into SQL: %q", value)
		}
	}
	for _, fragment := range []string{"$1", "$2", "$3", "$4", "status_code >= 400", "users.name"} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("filter SQL is missing %q: %s", fragment, where)
		}
	}
}

func TestNormalizeModelCatalogCreatesStableMuhiyaCodeContract(t *testing.T) {
	model := Model{
		ID:                          "model-minimax",
		Name:                        "minimax-m3",
		Status:                      "active",
		MuhiyaCodeVisible:           true,
		InputCostNanoPerMillion:     money.NanoUSD(300_000_000),
		OutputCostNanoPerMillion:    money.NanoUSD(1_200_000_000),
		CacheReadCostNanoPerMillion: money.NanoUSD(30_000_000),
		PricingTiers: []pricing.Tier{{
			MinInputTokensExclusive: 512_000,
			Rates: pricing.Rates{
				InputPerMillion:  money.NanoUSD(600_000_000),
				OutputPerMillion: money.NanoUSD(2_400_000_000),
			},
		}},
	}

	if err := normalizeModelCatalog(&model); err != nil {
		t.Fatal(err)
	}
	if model.ProviderFamily != "minimax-openrouter" {
		t.Fatalf("provider family = %q", model.ProviderFamily)
	}
	if model.PricingRuleSetID != "pricing:model-minimax" {
		t.Fatalf("pricing rule set = %q", model.PricingRuleSetID)
	}
	if !strings.Contains(model.CacheContract, `"route_scoped":true`) {
		t.Fatalf("unexpected cache contract: %s", model.CacheContract)
	}
	if len(model.Tags) != 1 || model.Tags[0] != "muhiyacode" {
		t.Fatalf("unexpected tags: %#v", model.Tags)
	}
}

func TestNormalizeModelCatalogRejectsMalformedCacheContract(t *testing.T) {
	model := Model{
		ID:               "model-bad",
		Name:             "bad",
		Status:           "active",
		CacheContract:    "{not-json}",
		PricingRuleSetID: "pricing:model-bad",
	}
	err := normalizeModelCatalog(&model)
	if !errors.Is(err, ErrInvalidModelConfig) {
		t.Fatalf("got %v, want ErrInvalidModelConfig", err)
	}
}
