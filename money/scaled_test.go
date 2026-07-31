package money

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestCostForTokensScaledAppliesExactMultiplier(t *testing.T) {
	base, err := CostForTokens(1_000_000, 300_000_000)
	if err != nil {
		t.Fatal(err)
	}
	doubled, err := CostForTokensScaled(1_000_000, 300_000_000, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if doubled != base*2 {
		t.Fatalf("2x scale = %d, want %d", doubled, base*2)
	}
	halved, err := CostForTokensScaled(1_000_000, 300_000_000, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if halved != base/2 {
		t.Fatalf("half scale = %d, want %d", halved, base/2)
	}
}

func TestCostForTokensIsTheUnscaledCase(t *testing.T) {
	for _, tokens := range []int64{0, 1, 7, 999_999, 1_000_000, 3_500_000} {
		for _, rate := range []NanoUSD{0, 1, 300_000_000, 2_400_000_000} {
			plain, plainErr := CostForTokens(tokens, rate)
			scaled, scaledErr := CostForTokensScaled(tokens, rate, 1, 1)
			if plainErr != nil || scaledErr != nil {
				t.Fatalf("tokens=%d rate=%d: %v / %v", tokens, rate, plainErr, scaledErr)
			}
			if plain != scaled {
				t.Fatalf("tokens=%d rate=%d: plain %d != scaled %d", tokens, rate, plain, scaled)
			}
		}
	}
}

// TestCostForTokensScaledRoundsOnlyOnce is the reason this function exists.
// Scaling the rate first and costing second rounds twice and overcharges; the
// correct result is a single ceiling over the whole rational expression.
func TestCostForTokensScaledRoundsOnlyOnce(t *testing.T) {
	// 3 tokens at 1 nano-USD/M tripled: ceil(3*1*3 / 1e6) = 1 nano-USD.
	// The double-rounding path would give ceil(ceil(1*3)) style inflation.
	got, err := CostForTokensScaled(3, 1, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("got %d, want 1", got)
	}

	// A rate that does not divide evenly under the multiplier still ceils once.
	// ceil(1_000_001 * 300_000_000 * 1 / (1e6 * 3)) = ceil(100000100.0) = 100000100.
	got, err = CostForTokensScaled(1_000_001, 300_000_000, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := referenceCeil(1_000_001, 300_000_000, 1, 3)
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestCostForTokensScaledMatchesBigRatReference(t *testing.T) {
	tokenCounts := []int64{1, 2, 17, 4095, 65_537, 1_000_001, 12_345_678}
	rates := []NanoUSD{1, 3, 60_000_000, 300_000_000, 1_250_000_000, 2_400_000_001}
	scales := [][2]int64{{1, 1}, {2, 1}, {1, 2}, {3, 7}, {7, 3}, {100, 99}}

	for _, tokens := range tokenCounts {
		for _, rate := range rates {
			for _, scale := range scales {
				got, err := CostForTokensScaled(tokens, rate, scale[0], scale[1])
				if err != nil {
					t.Fatalf("tokens=%d rate=%d scale=%v: %v", tokens, rate, scale, err)
				}
				want := referenceCeil(tokens, rate, scale[0], scale[1])
				if got != want {
					t.Fatalf("tokens=%d rate=%d scale=%v: got %d want %d",
						tokens, rate, scale, got, want)
				}
			}
		}
	}
}

// referenceCeil is an independent big.Rat implementation of the pricing
// formula, used to prove the big.Int path introduces no drift.
func referenceCeil(tokens int64, rate NanoUSD, num, den int64) NanoUSD {
	value := new(big.Rat).SetFrac(big.NewInt(tokens), big.NewInt(1))
	value.Mul(value, new(big.Rat).SetFrac(big.NewInt(int64(rate)), big.NewInt(1)))
	value.Mul(value, new(big.Rat).SetFrac(big.NewInt(num), big.NewInt(den)))
	value.Quo(value, new(big.Rat).SetFrac(big.NewInt(PerMillion), big.NewInt(1)))

	quotient := new(big.Int).Quo(value.Num(), value.Denom())
	if new(big.Int).Mul(quotient, value.Denom()).Cmp(value.Num()) != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return NanoUSD(quotient.Int64())
}

func TestCostForTokensScaledRejectsInvalidScale(t *testing.T) {
	if _, err := CostForTokensScaled(1, 1, 1, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero denominator: %v", err)
	}
	if _, err := CostForTokensScaled(1, 1, 1, -2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative denominator: %v", err)
	}
	if _, err := CostForTokensScaled(1, 1, -1, 1); !errors.Is(err, ErrNegative) {
		t.Fatalf("negative numerator: %v", err)
	}
	if _, err := CostForTokensScaled(-1, 1, 1, 1); !errors.Is(err, ErrNegative) {
		t.Fatalf("negative tokens: %v", err)
	}
}

func TestCostForTokensScaledOverflows(t *testing.T) {
	_, err := CostForTokensScaled(math.MaxInt64, NanoUSD(math.MaxInt64), math.MaxInt64, 1)
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("want overflow, got %v", err)
	}
}

// A zero multiplier is a legitimate "this window is free" configuration.
func TestCostForTokensScaledZeroNumeratorIsFree(t *testing.T) {
	got, err := CostForTokensScaled(1_000_000, 300_000_000, 0, 1)
	if err != nil || got != 0 {
		t.Fatalf("got %d, %v", got, err)
	}
}
