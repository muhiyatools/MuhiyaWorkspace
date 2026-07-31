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
	usage, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:     1_000_000,
		OutputTokens:     100_000,
		CacheReadTokens:  400_000,
		CacheWriteTokens: 100_000,
	}, PromptInclusive)
	if anomaly != AnomalyNone {
		t.Fatalf("unexpected anomaly: %s", anomaly)
	}

	quote, err := testRuleSet(t).Quote(usage)
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
	atBoundary, err := rules.Quote(InputUsage(512_000))
	if err != nil {
		t.Fatal(err)
	}
	aboveBoundary, err := rules.Quote(InputUsage(512_001))
	if err != nil {
		t.Fatal(err)
	}
	if atBoundary.Rates.InputPerMillion != 300_000_000 {
		t.Fatal("boundary must use base tier")
	}
	if aboveBoundary.Rates.InputPerMillion != 600_000_000 {
		t.Fatal("above boundary must use high tier")
	}
	if atBoundary.Receipt.TierThreshold != BaseTierThreshold {
		t.Fatalf("boundary tier threshold = %d, want base", atBoundary.Receipt.TierThreshold)
	}
	if aboveBoundary.Receipt.TierThreshold != 512_000 {
		t.Fatalf("above-boundary tier threshold = %d, want 512000", aboveBoundary.Receipt.TierThreshold)
	}
}

// The tier is selected by TOTAL prompt size, not by the uncached remainder.
// A 600K-token prompt served almost entirely from cache is still a 600K-token
// prompt as far as the provider's context pricing is concerned.
func TestTierSelectionUsesTotalPromptNotFreshRemainder(t *testing.T) {
	rules := testRuleSet(t)
	usage, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:    600_000,
		CacheReadTokens: 599_000,
	}, PromptInclusive)
	if anomaly != AnomalyNone {
		t.Fatalf("unexpected anomaly: %s", anomaly)
	}
	quote, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Receipt.TierThreshold != 512_000 {
		t.Fatalf("tier threshold = %d, want the 512000 tier to apply",
			quote.Receipt.TierThreshold)
	}
}

func TestMaxOutputTokensNeverExceedsBudget(t *testing.T) {
	rules := testRuleSet(t)
	max, err := rules.MaxOutputTokens(InputUsage(1000), 100_000, money.NanoUSD(10_000_000))
	if err != nil {
		t.Fatal(err)
	}
	usage := InputUsage(1000)
	usage.OutputTokens = max
	allowed, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Cost > 10_000_000 {
		t.Fatalf("allowed quote %s exceeds budget", allowed.Cost)
	}
	if max < 100_000 {
		usage.OutputTokens = max + 1
		next, err := rules.Quote(usage)
		if err != nil {
			t.Fatal(err)
		}
		if next.Cost <= 10_000_000 {
			t.Fatalf("max output %d was not maximal", max)
		}
	}
}

// The receipt must be self-consistent: its lines are the derivation of its
// total, and a total that does not equal its parts is not an audit trail.
func TestReceiptLinesSumToTotal(t *testing.T) {
	usage, _ := NormalizeUsage(ReportedUsage{
		PromptTokens:     900_000,
		OutputTokens:     50_000,
		CacheReadTokens:  300_000,
		CacheWriteTokens: 20_000,
	}, PromptInclusive)
	quote, err := testRuleSet(t).Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	var sum money.NanoUSD
	for _, line := range quote.Receipt.Lines {
		sum += line.Cost
	}
	if sum != quote.Receipt.Total {
		t.Fatalf("lines sum to %d but total is %d", sum, quote.Receipt.Total)
	}
	if quote.Receipt.Total != quote.Cost {
		t.Fatalf("receipt total %d != quote cost %d", quote.Receipt.Total, quote.Cost)
	}
	if len(quote.Receipt.Lines) != 4 {
		t.Fatalf("expected 4 non-zero lines, got %d", len(quote.Receipt.Lines))
	}
}

func TestZeroTokenClassesProduceNoLines(t *testing.T) {
	quote, err := testRuleSet(t).Quote(Usage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(quote.Receipt.Lines) != 0 {
		t.Fatalf("expected no lines, got %d", len(quote.Receipt.Lines))
	}
	if quote.Cost != 0 {
		t.Fatalf("expected zero cost, got %d", quote.Cost)
	}
}
