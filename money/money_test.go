package money

import (
	"errors"
	"math"
	"testing"
)

func TestParseAndFormatUSD(t *testing.T) {
	cases := map[string]NanoUSD{
		"0":             0,
		"0.000000001":   1,
		"0.05":          50_000_000,
		"1.23":          1_230_000_000,
		"+12.000000009": 12_000_000_009,
	}
	for raw, want := range cases {
		got, err := ParseUSD(raw)
		if err != nil {
			t.Fatalf("ParseUSD(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseUSD(%q)=%d want %d", raw, got, want)
		}
		if reparsed, err := ParseUSD(got.String()); err != nil || reparsed != got {
			t.Fatalf("round trip %q -> %q -> %d, %v", raw, got.String(), reparsed, err)
		}
	}
}

func TestParseUSDRejectsInvalidOrExcessPrecision(t *testing.T) {
	for _, raw := range []string{"", "-1", ".5", "1.", "1e3", "0.0000000001", "abc"} {
		if _, err := ParseUSD(raw); err == nil {
			t.Fatalf("ParseUSD(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestCompatibilityConversions(t *testing.T) {
	got, err := FromUSD(0.05)
	if err != nil || got != 50_000_000 {
		t.Fatalf("FromUSD: %d, %v", got, err)
	}
	credits, err := FromCredits(3.5)
	if err != nil || credits != 35_000_000 {
		t.Fatalf("FromCredits: %d, %v", credits, err)
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := FromUSD(value); err == nil {
			t.Fatalf("FromUSD(%v) unexpectedly succeeded", value)
		}
	}
}

func TestCostForTokensCeilsAndChecks(t *testing.T) {
	got, err := CostForTokens(1, 300_000_000)
	if err != nil || got != 300 {
		t.Fatalf("one token: %d, %v", got, err)
	}
	got, err = CostForTokens(1_000_000, 300_000_000)
	if err != nil || got != 300_000_000 {
		t.Fatalf("one million tokens: %d, %v", got, err)
	}
	if _, err := CostForTokens(-1, 1); !errors.Is(err, ErrNegative) {
		t.Fatalf("negative tokens: %v", err)
	}
	if _, err := CostForTokens(math.MaxInt64, NanoUSD(math.MaxInt64)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow: %v", err)
	}
}

func TestMulDivCeilForDurationPricing(t *testing.T) {
	got, err := MulDivCeil(1500, 6_000_000, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 150_000 {
		t.Fatalf("got %d nano-USD, want 150000", got)
	}
}

func TestAddSubOverflow(t *testing.T) {
	if _, err := Add(NanoUSD(math.MaxInt64), 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add overflow: %v", err)
	}
	if got, err := Sub(10, 3); err != nil || got != 7 {
		t.Fatalf("Sub: %d, %v", got, err)
	}
}
