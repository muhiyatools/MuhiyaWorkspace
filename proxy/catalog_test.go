package proxy

import (
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
