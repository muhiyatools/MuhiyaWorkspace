package proxy

import (
	"testing"

	"time"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

// These tests encode the pricing OpenRouter publishes for the three catalog
// models and run it through the real rule-set resolution, so the configuration
// in db/queries/configure_openrouter_pricing.sql is verified rather than
// assumed. If OpenRouter reprices a model, a test here should fail before a
// customer notices.

func nano(usd float64) money.NanoUSD {
	return money.NanoUSD(usd * 1e9)
}

func nanoPtr(usd float64) *money.NanoUSD {
	value := nano(usd)
	return &value
}

// gpt-5.6-terra: in $1, out $6, cached-in $0.10,
// cache write <=272K $1.25, >272K $2.50.
func terraModel() *db.Model {
	return &db.Model{
		ID: "terra", Name: "gpt-5.6-terra",
		PromptAccounting:             string(pricing.PromptInclusive),
		InputCostNanoPerMillion:      nano(1.00),
		OutputCostNanoPerMillion:     nano(6.00),
		CacheReadCostNanoPerMillion:  nano(0.10),
		CacheWriteCostNanoPerMillion: nano(1.25),
		PricingTiers: []pricing.Tier{{
			MinInputTokensExclusive: 272_000,
			Rates: pricing.Rates{
				InputPerMillion:      nano(1.00),
				OutputPerMillion:     nano(6.00),
				CacheReadPerMillion:  nano(0.10),
				CacheWritePerMillion: nano(2.50),
			},
		}},
	}
}

// gpt-5.6-luna: the same shape at one tenth the price.
func lunaModel() *db.Model {
	return &db.Model{
		ID: "luna", Name: "gpt-5.6-luna",
		PromptAccounting:             string(pricing.PromptInclusive),
		InputCostNanoPerMillion:      nano(0.10),
		OutputCostNanoPerMillion:     nano(0.60),
		CacheReadCostNanoPerMillion:  nano(0.01),
		CacheWriteCostNanoPerMillion: nano(0.125),
		PricingTiers: []pricing.Tier{{
			MinInputTokensExclusive: 272_000,
			Rates: pricing.Rates{
				InputPerMillion:      nano(0.10),
				OutputPerMillion:     nano(0.60),
				CacheReadPerMillion:  nano(0.01),
				CacheWritePerMillion: nano(0.25),
			},
		}},
	}
}

// qwen3.7-flash: flat input/output, but three context bands for the 5-minute
// cache rates - the case the flat model columns cannot express at all.
func qwenFlashModel() *db.Model {
	tierRates := func(write float64) pricing.Rates {
		return pricing.Rates{
			InputPerMillion:      nano(0.03),
			OutputPerMillion:     nano(0.13),
			CacheReadPerMillion:  nano(0.006),
			CacheWritePerMillion: nano(write),
		}
	}
	threshold32k := int64(32_000)
	threshold256k := int64(256_000)
	return &db.Model{
		ID: "qwen", Name: "qwen3.7-flash",
		PromptAccounting:             string(pricing.PromptInclusive),
		InputCostNanoPerMillion:      nano(0.03),
		OutputCostNanoPerMillion:     nano(0.13),
		CacheReadCostNanoPerMillion:  nano(0.006),
		CacheWriteCostNanoPerMillion: nano(0.038),
		PricingTiers: []pricing.Tier{
			{MinInputTokensExclusive: 32_000, Rates: tierRates(0.125)},
			{MinInputTokensExclusive: 256_000, Rates: tierRates(0.25)},
		},
		CacheTTLRates: []pricing.TTLRate{
			{TTL: pricing.TTL5m, CacheReadPerMillion: nanoPtr(0.003), CacheWritePerMillion: nanoPtr(0.038)},
			{MinInputTokensExclusive: &threshold32k, TTL: pricing.TTL5m,
				CacheReadPerMillion: nanoPtr(0.01), CacheWritePerMillion: nanoPtr(0.125)},
			{MinInputTokensExclusive: &threshold256k, TTL: pricing.TTL5m,
				CacheReadPerMillion: nanoPtr(0.02), CacheWritePerMillion: nanoPtr(0.25)},
		},
	}
}

func ratesAt(t *testing.T, model *db.Model, promptTokens int64) pricing.Rates {
	t.Helper()
	rules, err := pricingRulesForModel(model)
	if err != nil {
		t.Fatalf("%s: %v", model.Name, err)
	}
	rates, _ := rules.RatesForPromptTotal(promptTokens)
	return rates
}

func assertRate(t *testing.T, label string, got money.NanoUSD, wantUSD float64) {
	t.Helper()
	if got != nano(wantUSD) {
		t.Errorf("%s = %s, want $%.6f/M", label, got, wantUSD)
	}
}

func TestTerraMatchesPublishedPricing(t *testing.T) {
	model := terraModel()

	small := ratesAt(t, model, 100_000)
	assertRate(t, "terra input", small.RateFor(pricing.ClassInputFresh), 1.00)
	assertRate(t, "terra output", small.RateFor(pricing.ClassOutput), 6.00)
	assertRate(t, "terra cache read", small.RateFor(pricing.ClassCacheRead), 0.10)
	assertRate(t, "terra cache write <=272K", small.RateFor(pricing.ClassCacheWrite), 1.25)

	large := ratesAt(t, model, 300_000)
	assertRate(t, "terra cache write >272K", large.RateFor(pricing.ClassCacheWrite), 2.50)
	// Only the cache-write price moves across the boundary.
	assertRate(t, "terra input >272K", large.RateFor(pricing.ClassInputFresh), 1.00)
	assertRate(t, "terra output >272K", large.RateFor(pricing.ClassOutput), 6.00)
	assertRate(t, "terra cache read >272K", large.RateFor(pricing.ClassCacheRead), 0.10)
}

// "<=272K $1.25, >272K $2.50" means exactly 272,000 tokens is still the cheap
// band. An off-by-one here silently overcharges every prompt on the boundary.
func TestTerraTierBoundaryIsInclusiveOfTheCheaperBand(t *testing.T) {
	model := terraModel()
	assertRate(t, "terra at exactly 272K",
		ratesAt(t, model, 272_000).RateFor(pricing.ClassCacheWrite), 1.25)
	assertRate(t, "terra at 272K+1",
		ratesAt(t, model, 272_001).RateFor(pricing.ClassCacheWrite), 2.50)
}

func TestLunaMatchesPublishedPricing(t *testing.T) {
	model := lunaModel()

	small := ratesAt(t, model, 100_000)
	assertRate(t, "luna input", small.RateFor(pricing.ClassInputFresh), 0.10)
	assertRate(t, "luna output", small.RateFor(pricing.ClassOutput), 0.60)
	assertRate(t, "luna cache read", small.RateFor(pricing.ClassCacheRead), 0.01)
	assertRate(t, "luna cache write <=272K", small.RateFor(pricing.ClassCacheWrite), 0.125)

	assertRate(t, "luna cache write >272K",
		ratesAt(t, model, 300_000).RateFor(pricing.ClassCacheWrite), 0.25)
}

// Qwen is the model that actually needs the TTL layer: its 5-minute cache
// rates differ from its cached-input rate AND move across three context bands.
func TestQwenFlashFiveMinuteCacheBands(t *testing.T) {
	model := qwenFlashModel()

	cases := []struct {
		name        string
		prompt      int64
		read5m      float64
		write5m     float64
		cachedInput float64
	}{
		{"<=32K", 10_000, 0.003, 0.038, 0.006},
		{"32K..256K", 100_000, 0.01, 0.125, 0.006},
		{">256K", 300_000, 0.02, 0.25, 0.006},
	}
	for _, testCase := range cases {
		rates := ratesAt(t, model, testCase.prompt)
		assertRate(t, "qwen cache read 5m "+testCase.name,
			rates.RateFor(pricing.ClassCacheRead5m), testCase.read5m)
		assertRate(t, "qwen cache write 5m "+testCase.name,
			rates.RateFor(pricing.ClassCacheWrite5m), testCase.write5m)
		// The plain cached-input price is flat across every band.
		assertRate(t, "qwen cached input "+testCase.name,
			rates.RateFor(pricing.ClassCacheRead), testCase.cachedInput)
		assertRate(t, "qwen input "+testCase.name,
			rates.RateFor(pricing.ClassInputFresh), 0.03)
		assertRate(t, "qwen output "+testCase.name,
			rates.RateFor(pricing.ClassOutput), 0.13)
	}
}

// Qwen publishes no generic cache-write price, only a 5-minute one. The base
// cache-write rate is set to the 5-minute figure so an upstream that reports a
// flat cache_creation total is billed at that rate rather than falling through
// to the input rate.
func TestQwenFlatCacheWriteDoesNotFallBackToInputRate(t *testing.T) {
	rates := ratesAt(t, qwenFlashModel(), 10_000)
	assertRate(t, "qwen flat cache write", rates.RateFor(pricing.ClassCacheWrite), 0.038)
	if rates.RateFor(pricing.ClassCacheWrite) == rates.RateFor(pricing.ClassInputFresh) {
		t.Error("flat cache write fell back to the input rate")
	}
}

// Terra and Luna have no 5-minute rows, so their 5m classes must inherit the
// tier's cache-write price. Inheritance here is correct, not a gap.
func TestTerraAndLunaInheritFiveMinuteRates(t *testing.T) {
	for _, testCase := range []struct {
		model            *db.Model
		smallWrite       float64
		largeWrite       float64
		cachedInputPrice float64
	}{
		{terraModel(), 1.25, 2.50, 0.10},
		{lunaModel(), 0.125, 0.25, 0.01},
	} {
		small := ratesAt(t, testCase.model, 100_000)
		assertRate(t, testCase.model.Name+" 5m write inherits",
			small.RateFor(pricing.ClassCacheWrite5m), testCase.smallWrite)
		assertRate(t, testCase.model.Name+" 1h write inherits",
			small.RateFor(pricing.ClassCacheWrite1h), testCase.smallWrite)
		assertRate(t, testCase.model.Name+" 5m read inherits",
			small.RateFor(pricing.ClassCacheRead5m), testCase.cachedInputPrice)

		large := ratesAt(t, testCase.model, 300_000)
		assertRate(t, testCase.model.Name+" 5m write inherits above tier",
			large.RateFor(pricing.ClassCacheWrite5m), testCase.largeWrite)
	}
}

// A full request priced end to end, as a sanity check that the parts compose.
func TestQwenFlashEndToEndCharge(t *testing.T) {
	entry := &db.RequestLog{ID: "req-qwen"}
	setCalculatedCost(entry, qwenFlashModel(), pricing.ReportedUsage{
		PromptTokens:       100_000, // lands in the 32K..256K band
		OutputTokens:       10_000,
		CacheReadTokens:    40_000, // plain cached input at $0.006
		CacheWrite5mTokens: 20_000, // 5-minute creation at $0.125
	}, time.Time{})

	// fresh input 40,000 * $0.03  =   1_200_000 nano
	// output      10,000 * $0.13  =   1_300_000
	// cache read  40,000 * $0.006 =     240_000
	// write 5m    20,000 * $0.125 =   2_500_000
	want := money.NanoUSD(1_200_000 + 1_300_000 + 240_000 + 2_500_000)
	if entry.CostNanoUSD != want {
		t.Fatalf("charge = %d, want %d", entry.CostNanoUSD, want)
	}
	if entry.PricingTierThreshold == nil || *entry.PricingTierThreshold != 32_000 {
		t.Fatalf("tier threshold = %v, want 32000", entry.PricingTierThreshold)
	}
}
