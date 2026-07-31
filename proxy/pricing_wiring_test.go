package proxy

import (
	"net/http/httptest"
	"testing"
	"time"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

func anthropicModel() *db.Model {
	return &db.Model{
		ID:                           "claude",
		Name:                         "claude-sonnet",
		PromptAccounting:             string(pricing.PromptExclusive),
		InputCostNanoPerMillion:      3_000_000_000,
		OutputCostNanoPerMillion:     15_000_000_000,
		CacheReadCostNanoPerMillion:  300_000_000,
		CacheWriteCostNanoPerMillion: 3_750_000_000,
	}
}

func openAIModel() *db.Model {
	model := anthropicModel()
	model.ID = "gpt"
	model.Name = "gpt-5"
	model.PromptAccounting = string(pricing.PromptInclusive)
	return model
}

// The end-to-end form of the G1 regression: an Anthropic-shaped usage payload
// travelling through the real handler helper must bill its fresh input tokens.
func TestAnthropicUsageBillsFreshInputThroughHandler(t *testing.T) {
	entry := &db.RequestLog{ID: "req-1"}
	setCalculatedCost(entry, anthropicModel(), anthropicReportedUsage(&AnthropicUsage{
		InputTokens:          500,
		OutputTokens:         100,
		CacheReadInputTokens: 40_000,
	}), time.Time{})

	// 500 * $3.00/M + 100 * $15.00/M + 40,000 * $0.30/M
	want := money.NanoUSD(1_500_000 + 1_500_000 + 12_000_000)
	if entry.CostNanoUSD != want {
		t.Fatalf("cost = %d, want %d", entry.CostNanoUSD, want)
	}
	// input_tokens is normalized to the total prompt, so the column means the
	// same thing regardless of which dialect the upstream speaks.
	if entry.InputTokens != 40_500 {
		t.Fatalf("input tokens = %d, want the 40500 canonical total", entry.InputTokens)
	}
	if entry.PromptAccounting != string(pricing.PromptExclusive) {
		t.Fatalf("accounting = %q", entry.PromptAccounting)
	}
}

// The same underlying request reported in either dialect must cost the same.
func TestBothDialectsCostTheSameThroughHandler(t *testing.T) {
	anthropic := &db.RequestLog{ID: "req-anthropic"}
	setCalculatedCost(anthropic, anthropicModel(), anthropicReportedUsage(&AnthropicUsage{
		InputTokens:          500,
		OutputTokens:         100,
		CacheReadInputTokens: 40_000,
	}), time.Time{})

	openAI := &db.RequestLog{ID: "req-openai"}
	setCalculatedCost(openAI, openAIModel(), pricing.ReportedUsage{
		PromptTokens:    40_500,
		OutputTokens:    100,
		CacheReadTokens: 40_000,
	}, time.Time{})

	if anthropic.CostNanoUSD != openAI.CostNanoUSD {
		t.Fatalf("same request priced differently: anthropic=%d openai=%d",
			anthropic.CostNanoUSD, openAI.CostNanoUSD)
	}
	if anthropic.InputTokens != openAI.InputTokens {
		t.Fatalf("input tokens differ: %d vs %d", anthropic.InputTokens, openAI.InputTokens)
	}
}

// Every charge must arrive with the derivation that produced it.
func TestPricingReceiptIsRecordedOnTheLog(t *testing.T) {
	entry := &db.RequestLog{ID: "req-receipt"}
	setCalculatedCost(entry, openAIModel(), pricing.ReportedUsage{
		PromptTokens:     10_000,
		OutputTokens:     2_000,
		CacheReadTokens:  4_000,
		CacheWriteTokens: 1_000,
	}, time.Time{})

	if entry.PricingRuleSetID == "" {
		t.Fatal("no rule set id recorded")
	}
	if len(entry.PricingLines) != 4 {
		t.Fatalf("expected 4 pricing lines, got %d", len(entry.PricingLines))
	}
	var sum money.NanoUSD
	for _, line := range entry.PricingLines {
		sum += line.Cost
	}
	if sum != entry.CostNanoUSD {
		t.Fatalf("lines sum to %d but the charge is %d", sum, entry.CostNanoUSD)
	}
	if entry.PriceMultiplierNum != 1 || entry.PriceMultiplierDen != 1 {
		t.Fatalf("unscaled request recorded multiplier %d/%d",
			entry.PriceMultiplierNum, entry.PriceMultiplierDen)
	}
}

func TestUsageAnomalyIsRecorded(t *testing.T) {
	entry := &db.RequestLog{ID: "req-anomaly"}
	setCalculatedCost(entry, openAIModel(), pricing.ReportedUsage{
		PromptTokens:    100,
		CacheReadTokens: 40_000,
	}, time.Time{})

	if entry.UsageAnomaly != string(pricing.AnomalyPromptBelowCache) {
		t.Fatalf("anomaly = %q, want it flagged", entry.UsageAnomaly)
	}
}

// Anthropic's per-lifetime cache-write breakdown must reach pricing, and the
// flat total must not be double counted alongside it.
func TestAnthropicCacheCreationSplitsByTTL(t *testing.T) {
	usage := &AnthropicUsage{InputTokens: 100, CacheCreationInputTokens: 1_000}
	usage.ApplyCacheCreation(&AnthropicCacheCreation{
		Ephemeral5mInputTokens: 600,
		Ephemeral1hInputTokens: 400,
	})
	reported := anthropicReportedUsage(usage)

	if reported.CacheWrite5mTokens != 600 || reported.CacheWrite1hTokens != 400 {
		t.Fatalf("ttl split = %d/%d", reported.CacheWrite5mTokens, reported.CacheWrite1hTokens)
	}
	if reported.CacheWriteTokens != 0 {
		t.Fatalf("flat remainder = %d, want 0 (the breakdown already covers it)",
			reported.CacheWriteTokens)
	}
}

func TestAnthropicCacheCreationFlatOnlyStillCounts(t *testing.T) {
	reported := anthropicReportedUsage(&AnthropicUsage{
		InputTokens: 100, CacheCreationInputTokens: 1_000,
	})
	if reported.CacheWriteTokens != 1_000 {
		t.Fatalf("flat write = %d, want 1000", reported.CacheWriteTokens)
	}
}

func TestParseAnthropicCacheCreationAbsent(t *testing.T) {
	if got := ParseAnthropicCacheCreation(map[string]interface{}{}); got != nil {
		t.Fatalf("expected nil for a payload with no cache_creation, got %+v", got)
	}
}

// The pricing clock must be pinned once and reused, so a request cannot be
// quoted off-peak and settled at peak.
func TestPricedAtIsPinnedOncePerRequest(t *testing.T) {
	request := pinPricedAt(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	first := pricedAtFrom(request.Context())

	// Re-pinning (a retry, a nested call) must not move the instant.
	again := pinPricedAt(request)
	if got := pricedAtFrom(again.Context()); !got.Equal(first) {
		t.Fatalf("re-pinning moved the instant from %s to %s", first, got)
	}

	time.Sleep(2 * time.Millisecond)
	if got := pricedAtFrom(request.Context()); !got.Equal(first) {
		t.Fatalf("pinned instant drifted to %s", got)
	}
}

// A request that never went through admission still has to price; falling back
// to the zero time would silently disable every peak window.
func TestPricedAtFallsBackToTheClock(t *testing.T) {
	if pricedAtFrom(nil).IsZero() {
		t.Fatal("fallback must not be the zero time")
	}
}

// A model with a peak window is billed at the pinned instant's rate.
func TestPeakWindowAppliesThroughHandler(t *testing.T) {
	model := openAIModel()
	model.PriceWindows = []pricing.Window{{
		ID: "peak", Label: "Peak", StartMinuteUTC: 0, EndMinuteUTC: pricing.MinutesPerDay,
		WeekdayMask: 127, MultiplierNum: 2, MultiplierDen: 1,
	}}

	peak := &db.RequestLog{ID: "req-peak"}
	setCalculatedCost(peak, model, pricing.ReportedUsage{PromptTokens: 1_000_000},
		time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC))

	unpinned := &db.RequestLog{ID: "req-unpinned"}
	setCalculatedCost(unpinned, openAIModel(),
		pricing.ReportedUsage{PromptTokens: 1_000_000}, time.Time{})

	if peak.CostNanoUSD != unpinned.CostNanoUSD*2 {
		t.Fatalf("peak %d is not double the base %d", peak.CostNanoUSD, unpinned.CostNanoUSD)
	}
	if peak.PriceWindowID != "peak" {
		t.Fatalf("window id = %q", peak.PriceWindowID)
	}
}

// A changed TTL rate or price window must produce a different rule-set id,
// otherwise two requests billed at different rates would claim the same
// snapshot and the audit trail would be false.
func TestRuleSetIDCoversEveryPricingInput(t *testing.T) {
	base, err := pricingRulesForModel(openAIModel())
	if err != nil {
		t.Fatal(err)
	}

	withWindow := openAIModel()
	withWindow.PriceWindows = []pricing.Window{{
		ID: "peak", StartMinuteUTC: 0, EndMinuteUTC: pricing.MinutesPerDay,
		WeekdayMask: 127, MultiplierNum: 2, MultiplierDen: 1,
	}}
	windowed, err := pricingRulesForModel(withWindow)
	if err != nil {
		t.Fatal(err)
	}
	if windowed.ID == base.ID {
		t.Fatal("adding a price window did not change the rule set id")
	}

	rate := money.NanoUSD(1_250_000_000)
	withTTL := openAIModel()
	withTTL.CacheTTLRates = []pricing.TTLRate{{
		TTL: pricing.TTL5m, CacheWritePerMillion: &rate,
	}}
	ttled, err := pricingRulesForModel(withTTL)
	if err != nil {
		t.Fatal(err)
	}
	if ttled.ID == base.ID {
		t.Fatal("adding a TTL rate did not change the rule set id")
	}

	withAccounting := openAIModel()
	withAccounting.PromptAccounting = string(pricing.PromptExclusive)
	accounted, err := pricingRulesForModel(withAccounting)
	if err != nil {
		t.Fatal(err)
	}
	if accounted.ID == base.ID {
		t.Fatal("changing the accounting mode did not change the rule set id")
	}
}

// A model with no TTL rows and no windows must price exactly as before.
func TestUnconfiguredModelPricesUnchanged(t *testing.T) {
	entry := &db.RequestLog{ID: "req-plain"}
	setCalculatedCost(entry, openAIModel(), pricing.ReportedUsage{
		PromptTokens:     1_000_000,
		OutputTokens:     100_000,
		CacheReadTokens:  400_000,
		CacheWriteTokens: 100_000,
	}, time.Time{})

	// 500k fresh * $3.00 + 100k out * $15.00 + 400k read * $0.30 + 100k write * $3.75
	want := money.NanoUSD(1_500_000_000 + 1_500_000_000 + 120_000_000 + 375_000_000)
	if entry.CostNanoUSD != want {
		t.Fatalf("cost = %d, want %d", entry.CostNanoUSD, want)
	}
}
