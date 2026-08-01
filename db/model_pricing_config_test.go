package db

import (
	"errors"
	"testing"

	"gateway/money"
	"gateway/pricing"
)

func baseModelForPricing() Model {
	return Model{
		ID:                           "m1",
		Name:                         "test-model",
		PricingRuleSetID:             "pricing:m1",
		InputCostNanoPerMillion:      3_000_000_000,
		OutputCostNanoPerMillion:     15_000_000_000,
		CacheReadCostNanoPerMillion:  300_000_000,
		CacheWriteCostNanoPerMillion: 3_750_000_000,
		PromptAccounting:             string(pricing.PromptInclusive),
	}
}

func TestValidatePricingRulesAcceptsAWellFormedWindow(t *testing.T) {
	model := baseModelForPricing()
	model.PriceWindows = []pricing.Window{{
		Label: "Peak", StartMinuteUTC: 16 * 60, EndMinuteUTC: 20 * 60,
		MultiplierNum: 2, MultiplierDen: 1,
	}}
	if err := validateModelPricingRules(model); err != nil {
		t.Fatalf("a valid window was rejected: %v", err)
	}
}

// Two new windows arrive with no ids at all. Placeholders must not collide,
// or a perfectly valid save would fail as a duplicate.
func TestValidatePricingRulesAcceptsMultipleUnsavedWindows(t *testing.T) {
	model := baseModelForPricing()
	model.PriceWindows = []pricing.Window{
		{Label: "Peak", StartMinuteUTC: 16 * 60, EndMinuteUTC: 20 * 60, MultiplierNum: 2, MultiplierDen: 1},
		{Label: "Off-peak", StartMinuteUTC: 20 * 60, EndMinuteUTC: 16 * 60, MultiplierNum: 1, MultiplierDen: 2},
	}
	if err := validateModelPricingRules(model); err != nil {
		t.Fatalf("two unsaved windows were rejected: %v", err)
	}
}

// A window an operator half-filled in the admin form must still resolve: the
// denominator and weekday mask default rather than failing validation.
func TestValidatePricingRulesDefaultsHalfFilledWindow(t *testing.T) {
	model := baseModelForPricing()
	model.PriceWindows = []pricing.Window{{
		Label: "Peak", StartMinuteUTC: 0, EndMinuteUTC: 60, MultiplierNum: 2,
	}}
	if err := validateModelPricingRules(model); err != nil {
		t.Fatalf("half-filled window rejected: %v", err)
	}
}

func TestValidatePricingRulesRejectsBadWindows(t *testing.T) {
	cases := map[string]pricing.Window{
		"equal boundaries": {
			Label: "ambiguous", StartMinuteUTC: 600, EndMinuteUTC: 600,
			MultiplierNum: 2, MultiplierDen: 1,
		},
		"start out of range": {
			Label: "bad", StartMinuteUTC: 1440, EndMinuteUTC: 60,
			MultiplierNum: 2, MultiplierDen: 1,
		},
		"negative multiplier": {
			Label: "bad", StartMinuteUTC: 0, EndMinuteUTC: 60,
			MultiplierNum: -1, MultiplierDen: 1,
		},
		"unknown token class": {
			Label: "bad", StartMinuteUTC: 0, EndMinuteUTC: 60,
			MultiplierNum: 2, MultiplierDen: 1,
			AppliesTo: []pricing.TokenClass{"not_a_class"},
		},
	}
	for name, window := range cases {
		model := baseModelForPricing()
		model.PriceWindows = []pricing.Window{window}
		err := validateModelPricingRules(model)
		if err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
		if !errors.Is(err, ErrInvalidModelConfig) {
			t.Fatalf("%s: expected ErrInvalidModelConfig, got %v", name, err)
		}
	}
}

func TestValidatePricingRulesRejectsUnknownTTL(t *testing.T) {
	rate := money.NanoUSD(1_250_000_000)
	model := baseModelForPricing()
	model.CacheTTLRates = []pricing.TTLRate{{TTL: "30m", CacheWritePerMillion: &rate}}
	err := validateModelPricingRules(model)
	if err == nil || !errors.Is(err, ErrInvalidModelConfig) {
		t.Fatalf("expected an invalid-config error, got %v", err)
	}
}

func TestValidatePricingRulesAcceptsKnownTTLs(t *testing.T) {
	rate := money.NanoUSD(1_250_000_000)
	for _, ttl := range []string{pricing.TTL5m, pricing.TTL1h, pricing.TTLDefault} {
		model := baseModelForPricing()
		model.CacheTTLRates = []pricing.TTLRate{{TTL: ttl, CacheWritePerMillion: &rate}}
		if err := validateModelPricingRules(model); err != nil {
			t.Fatalf("ttl %q rejected: %v", ttl, err)
		}
	}
}

// A TTL rate scoped to a tier threshold must land on that tier, not the base.
func TestValidatePricingRulesOverlaysTTLOntoMatchingTier(t *testing.T) {
	threshold := int64(272_000)
	rate := money.NanoUSD(2_500_000_000)
	model := baseModelForPricing()
	model.PricingTiers = []pricing.Tier{{
		MinInputTokensExclusive: threshold,
		Rates:                   pricing.Rates{InputPerMillion: 6_000_000_000},
	}}
	model.CacheTTLRates = []pricing.TTLRate{{
		MinInputTokensExclusive: &threshold,
		TTL:                     pricing.TTL5m,
		CacheWritePerMillion:    &rate,
	}}
	if err := validateModelPricingRules(model); err != nil {
		t.Fatalf("tier-scoped TTL rate rejected: %v", err)
	}
}

func TestApplyModelCatalogDefaultsPicksAccountingFromFamily(t *testing.T) {
	anthropic := Model{ID: "a", Name: "claude-sonnet", Status: "active", ProviderFamily: "anthropic"}
	if err := applyModelCatalogDefaults(&anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic.PromptAccounting != string(pricing.PromptExclusive) {
		t.Fatalf("anthropic accounting = %q, want exclusive", anthropic.PromptAccounting)
	}

	other := Model{ID: "b", Name: "gpt-5", Status: "active", ProviderFamily: "openai-compatible"}
	if err := applyModelCatalogDefaults(&other); err != nil {
		t.Fatal(err)
	}
	if other.PromptAccounting != string(pricing.PromptInclusive) {
		t.Fatalf("openai accounting = %q, want inclusive", other.PromptAccounting)
	}
}

func TestApplyModelCatalogDefaultsRejectsUnknownAccounting(t *testing.T) {
	model := Model{ID: "c", Name: "x", Status: "active", PromptAccounting: "nonsense"}
	if err := applyModelCatalogDefaults(&model); err == nil {
		t.Fatal("expected an unknown accounting mode to be rejected")
	}
}

func TestApplyModelCatalogDefaultsKeepsExplicitAccounting(t *testing.T) {
	// An operator override must survive, even when it contradicts the family.
	model := Model{
		ID: "d", Name: "custom", Status: "active",
		ProviderFamily: "openai-compatible", PromptAccounting: string(pricing.PromptExclusive),
	}
	if err := applyModelCatalogDefaults(&model); err != nil {
		t.Fatal(err)
	}
	if model.PromptAccounting != string(pricing.PromptExclusive) {
		t.Fatalf("explicit accounting was overwritten to %q", model.PromptAccounting)
	}
}
