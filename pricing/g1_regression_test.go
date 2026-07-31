package pricing

import "testing"

// TestAnthropicFreshInputIsNotBilledAtZero is the G1 regression.
//
// Anthropic reports usage.input_tokens EXCLUDING cached tokens, while
// OpenAI/DeepSeek/OpenRouter report prompt_tokens INCLUDING them. Pricing that
// assumes one convention silently bills the other's fresh input at zero.
func TestAnthropicFreshInputIsNotBilledAtZero(t *testing.T) {
	rules, err := NewRuleSet("g1", Rates{
		InputPerMillion:      3_000_000_000,
		OutputPerMillion:     15_000_000_000,
		CacheReadPerMillion:    300_000_000,
		CacheWritePerMillion: 3_750_000_000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Anthropic-shaped: 500 fresh prompt tokens, 40k served from cache.
	usage, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:    500,
		CacheReadTokens: 40_000,
	}, PromptExclusive)
	if anomaly != "" {
		t.Fatalf("unexpected anomaly: %s", anomaly)
	}

	quote, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}

	// 500 tokens * $3.00/M = 1_500_000 nano-USD, plus 40k cache read
	// * $0.30/M = 12_000_000 nano-USD.
	const wantFreshInput = 1_500_000
	const wantCacheRead = 12_000_000
	if quote.Cost != wantFreshInput+wantCacheRead {
		t.Fatalf("cost = %d nano-USD, want %d; the fresh input tokens were not billed",
			quote.Cost, wantFreshInput+wantCacheRead)
	}
}

// TestBothAccountingConventionsAgree is the strongest correctness statement
// available: the same underlying reality, reported in either dialect, must
// produce byte-identical cost.
func TestBothAccountingConventionsAgree(t *testing.T) {
	rules, err := NewRuleSet("g1-agree", Rates{
		InputPerMillion:      3_000_000_000,
		OutputPerMillion:     15_000_000_000,
		CacheReadPerMillion:    300_000_000,
		CacheWritePerMillion: 3_750_000_000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// One reality: 40,500 prompt tokens of which 40,000 came from cache and
	// 500 were fresh; 1,000 output tokens.
	exclusive, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:    500, // Anthropic: fresh only
		OutputTokens:    1_000,
		CacheReadTokens: 40_000,
	}, PromptExclusive)
	if anomaly != "" {
		t.Fatalf("exclusive anomaly: %s", anomaly)
	}

	inclusive, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:    40_500, // OpenAI: total including cached
		OutputTokens:    1_000,
		CacheReadTokens: 40_000,
	}, PromptInclusive)
	if anomaly != "" {
		t.Fatalf("inclusive anomaly: %s", anomaly)
	}

	if exclusive != inclusive {
		t.Fatalf("normalized usage differs:\n exclusive=%+v\n inclusive=%+v", exclusive, inclusive)
	}

	exclusiveQuote, err := rules.Quote(exclusive)
	if err != nil {
		t.Fatal(err)
	}
	inclusiveQuote, err := rules.Quote(inclusive)
	if err != nil {
		t.Fatal(err)
	}
	if exclusiveQuote.Cost != inclusiveQuote.Cost {
		t.Fatalf("same reality priced differently: exclusive=%d inclusive=%d",
			exclusiveQuote.Cost, inclusiveQuote.Cost)
	}
}

// An inclusive provider reporting fewer prompt tokens than it claims to have
// served from cache is internally inconsistent. Clamping is correct, but it
// must be VISIBLE — silent clamping is how G1 stayed hidden.
func TestInconsistentInclusiveUsageIsFlagged(t *testing.T) {
	usage, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:    100,
		CacheReadTokens: 40_000,
	}, PromptInclusive)
	if anomaly == "" {
		t.Fatal("expected an anomaly to be reported")
	}
	if usage.FreshInputTokens != 0 {
		t.Fatalf("fresh input = %d, want clamp to 0", usage.FreshInputTokens)
	}
	if usage.PromptTotalTokens != 40_000 {
		t.Fatalf("prompt total = %d, want the cache floor 40000", usage.PromptTotalTokens)
	}
}
