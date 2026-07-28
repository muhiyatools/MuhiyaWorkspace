package pricing

import (
	"testing"

	"gateway/money"
)

func testRuleSet(t *testing.T) RuleSet {
	t.Helper()
	rules, err := NewRuleSet("minimax-m3:v1", Rates{
		InputPerMillion:     300_000_000,
		OutputPerMillion:    1_200_000_000,
		CacheReadPerMillion: 60_000_000,
	}, []Tier{{
		MinInputTokensExclusive: 512_000,
		Rates: Rates{
			InputPerMillion:     600_000_000,
			OutputPerMillion:    2_400_000_000,
			CacheReadPerMillion: 120_000_000,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestQuoteUsesExactCacheBreakdown(t *testing.T) {
	quote, err := testRuleSet(t).Quote(Usage{
		InputTokens:      1_000_000,
		OutputTokens:     100_000,
		CacheReadTokens:  400_000,
		CacheWriteTokens: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// High tier: .5M uncached input * .60 + .1M output * 2.40
	// + .4M cache read * .12 + .1M cache write fallback * .60 = $0.648.
	if quote.Cost != 648_000_000 {
		t.Fatalf("cost = %s, want 0.648", quote.Cost)
	}
}

func TestTierBoundaryIsExclusive(t *testing.T) {
	rules := testRuleSet(t)
	atBoundary, err := rules.Quote(Usage{InputTokens: 512_000})
	if err != nil {
		t.Fatal(err)
	}
	aboveBoundary, err := rules.Quote(Usage{InputTokens: 512_001})
	if err != nil {
		t.Fatal(err)
	}
	if atBoundary.Rates.InputPerMillion != 300_000_000 {
		t.Fatal("boundary must use base tier")
	}
	if aboveBoundary.Rates.InputPerMillion != 600_000_000 {
		t.Fatal("above boundary must use high tier")
	}
}

func TestMaxOutputTokensNeverExceedsBudget(t *testing.T) {
	rules := testRuleSet(t)
	max, err := rules.MaxOutputTokens(Usage{InputTokens: 1000}, 100_000, money.NanoUSD(10_000_000))
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := rules.Quote(Usage{InputTokens: 1000, OutputTokens: max})
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Cost > 10_000_000 {
		t.Fatalf("allowed quote %s exceeds budget", allowed.Cost)
	}
	if max < 100_000 {
		next, err := rules.Quote(Usage{InputTokens: 1000, OutputTokens: max + 1})
		if err != nil {
			t.Fatal(err)
		}
		if next.Cost <= 10_000_000 {
			t.Fatalf("max output %d was not maximal", max)
		}
	}
}
