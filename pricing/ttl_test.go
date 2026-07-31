package pricing

import "testing"

func ttlRates() Rates {
	return Rates{
		InputPerMillion:      3_000_000_000,
		OutputPerMillion:     15_000_000_000,
		CacheReadPerMillion:  300_000_000,
		CacheWritePerMillion: 3_750_000_000,
	}
}

// A model with no TTL rates configured must price exactly as it did before TTL
// support existed. This is the guarantee that makes the migration non-breaking.
func TestTTLRatesInheritWhenUnset(t *testing.T) {
	rates := ttlRates()
	if got := rates.RateFor(ClassCacheRead5m); got != rates.CacheReadPerMillion {
		t.Fatalf("cache_read_5m = %d, want inherit %d", got, rates.CacheReadPerMillion)
	}
	if got := rates.RateFor(ClassCacheWrite5m); got != rates.CacheWritePerMillion {
		t.Fatalf("cache_write_5m = %d, want inherit %d", got, rates.CacheWritePerMillion)
	}
	if got := rates.RateFor(ClassCacheWrite1h); got != rates.CacheWritePerMillion {
		t.Fatalf("cache_write_1h = %d, want inherit %d", got, rates.CacheWritePerMillion)
	}
}

func TestTTLRatesOverrideWhenSet(t *testing.T) {
	rates := ttlRates()
	rates.CacheRead5mPerMillion = 30_000_000
	rates.CacheWrite5mPerMillion = 1_250_000_000
	rates.CacheWrite1hPerMillion = 6_000_000_000

	cases := map[TokenClass]int64{
		ClassCacheRead5m:  30_000_000,
		ClassCacheWrite5m: 1_250_000_000,
		ClassCacheWrite1h: 6_000_000_000,
		ClassCacheRead:    300_000_000,
		ClassCacheWrite:   3_750_000_000,
	}
	for class, want := range cases {
		if got := int64(rates.RateFor(class)); got != want {
			t.Fatalf("%s = %d, want %d", class, got, want)
		}
	}
}

// Cache writes on a model with no configured write price fall back to the
// input rate rather than being given away free — long-standing behaviour that
// the TTL classes must also honour.
func TestCacheWriteFallsBackToInputRate(t *testing.T) {
	rates := Rates{InputPerMillion: 3_000_000_000, CacheReadPerMillion: 300_000_000}
	for _, class := range []TokenClass{ClassCacheWrite, ClassCacheWrite5m, ClassCacheWrite1h} {
		if got := rates.RateFor(class); got != rates.InputPerMillion {
			t.Fatalf("%s = %d, want the input rate %d", class, got, rates.InputPerMillion)
		}
	}
}

func TestQuotePricesEachTTLClassSeparately(t *testing.T) {
	rates := ttlRates()
	rates.CacheWrite5mPerMillion = 1_250_000_000
	rates.CacheWrite1hPerMillion = 6_000_000_000
	rules, err := NewRuleSet("ttl", rates, nil)
	if err != nil {
		t.Fatal(err)
	}

	usage, anomaly := NormalizeUsage(ReportedUsage{
		PromptTokens:       1_000_000,
		CacheWrite5mTokens: 400_000,
		CacheWrite1hTokens: 100_000,
	}, PromptInclusive)
	if anomaly != AnomalyNone {
		t.Fatalf("unexpected anomaly: %s", anomaly)
	}
	if usage.FreshInputTokens != 500_000 {
		t.Fatalf("fresh input = %d, want 500000", usage.FreshInputTokens)
	}

	quote, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	// 500k fresh * 3.00 + 400k * 1.25 + 100k * 6.00
	want := int64(1_500_000_000 + 500_000_000 + 600_000_000)
	if int64(quote.Cost) != want {
		t.Fatalf("cost = %d, want %d", quote.Cost, want)
	}

	byClass := map[TokenClass]int64{}
	for _, line := range quote.Receipt.Lines {
		byClass[line.Class] = int64(line.RatePerMillion)
	}
	if byClass[ClassCacheWrite5m] != 1_250_000_000 {
		t.Fatalf("5m line rate = %d", byClass[ClassCacheWrite5m])
	}
	if byClass[ClassCacheWrite1h] != 6_000_000_000 {
		t.Fatalf("1h line rate = %d", byClass[ClassCacheWrite1h])
	}
}

// TTL-split cache writes count toward the prompt total, so they can push a
// request into a higher context tier just like any other prompt tokens.
func TestTTLTokensCountTowardTierSelection(t *testing.T) {
	rules, err := NewRuleSet("ttl-tier", ttlRates(), []Tier{{
		MinInputTokensExclusive: 512_000,
		Rates:                   Rates{InputPerMillion: 6_000_000_000, CacheWritePerMillion: 7_500_000_000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	usage, _ := NormalizeUsage(ReportedUsage{
		PromptTokens:       100,
		CacheWrite5mTokens: 600_000,
	}, PromptExclusive)
	quote, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Receipt.TierThreshold != 512_000 {
		t.Fatalf("tier = %d, want the 512000 tier", quote.Receipt.TierThreshold)
	}
}
