package proxy

import (
	"bytes"
	"testing"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

func TestConservativeInputTokenBoundDoesNotPriceBytesAsTokens(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 180_000)
	model := &db.Model{ContextWindow: 1_000_000}

	got := conservativeInputTokenBound(body, 5_557, model)
	if got < 60_000 || got > 61_000 {
		t.Fatalf("bound=%d, want serialized estimate plus framing near 60,512", got)
	}
	rules, err := pricing.NewRuleSet("minimax-m3", pricing.Rates{
		InputPerMillion: 300_000_000, OutputPerMillion: 1_200_000_000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	usage := pricing.InputUsage(int64(got))
	usage.OutputTokens = 16_000
	quote, err := rules.Quote(usage)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Cost > money.NanoUSD(50_000_000) {
		t.Fatalf("quote=%s, want the representative request within the $0.05 window", quote.Cost)
	}
}

func TestSerializedTokenEstimatePricesUnicodeConservatively(t *testing.T) {
	const arabic = "مرحبا"
	got := serializedTokenEstimate([]byte(arabic))
	if got != 10 {
		t.Fatalf("estimate=%d, want two tokens per non-ASCII rune", got)
	}
}
