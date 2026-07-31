// Package money provides the exact monetary arithmetic used by admission,
// pricing and settled usage. One USD is represented as one billion
// nano-USD so sub-cent token prices remain exact without binary floating point.
package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

type NanoUSD int64

const (
	PerUSD     NanoUSD = 1_000_000_000
	PerCredit  NanoUSD = 10_000_000 // one legacy credit is USD 0.01
	PerMillion int64   = 1_000_000
)

var (
	ErrNegative = errors.New("money cannot be negative")
	ErrOverflow = errors.New("money overflow")
	ErrInvalid  = errors.New("invalid money value")
)

// FromUSD is a compatibility-boundary conversion for legacy JSON/admin fields.
// New financial logic must stay in NanoUSD after this conversion.
func FromUSD(value float64) (NanoUSD, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, ErrInvalid
	}
	if value < 0 {
		return 0, ErrNegative
	}
	scaled := value * float64(PerUSD)
	if scaled > float64(math.MaxInt64) {
		return 0, ErrOverflow
	}
	return NanoUSD(math.Round(scaled)), nil
}

// FromCredits converts the legacy "100 credits per USD" unit at the API
// boundary. Internally top-ups are stored and consumed as nano-USD.
func FromCredits(credits float64) (NanoUSD, error) {
	if math.IsNaN(credits) || math.IsInf(credits, 0) {
		return 0, ErrInvalid
	}
	if credits < 0 {
		return 0, ErrNegative
	}
	scaled := credits * float64(PerCredit)
	if scaled > float64(math.MaxInt64) {
		return 0, ErrOverflow
	}
	return NanoUSD(math.Round(scaled)), nil
}

// ParseUSD parses a non-negative fixed decimal with at most nine fractional
// digits. Exponents and excess precision are rejected so callers cannot
// accidentally introduce a rounding policy through text parsing.
func ParseUSD(raw string) (NanoUSD, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ErrInvalid
	}
	if strings.HasPrefix(raw, "+") {
		raw = strings.TrimPrefix(raw, "+")
	}
	if strings.HasPrefix(raw, "-") {
		return 0, ErrNegative
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalid
	}
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || whole > uint64(math.MaxInt64)/uint64(PerUSD) {
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return 0, ErrOverflow
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > 9 {
			return 0, ErrInvalid
		}
		for _, r := range fraction {
			if r < '0' || r > '9' {
				return 0, ErrInvalid
			}
		}
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	var fractional uint64
	if fraction != "" {
		fractional, err = strconv.ParseUint(fraction, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	total := whole*uint64(PerUSD) + fractional
	if total > uint64(math.MaxInt64) {
		return 0, ErrOverflow
	}
	return NanoUSD(total), nil
}

func (n NanoUSD) USD() float64 {
	return float64(n) / float64(PerUSD)
}

func (n NanoUSD) Credits() float64 {
	return float64(n) / float64(PerCredit)
}

func (n NanoUSD) String() string {
	if n < 0 {
		return "-" + NanoUSD(-n).String()
	}
	whole := int64(n / PerUSD)
	fraction := int64(n % PerUSD)
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%09d", whole, fraction)[:len(fmt.Sprintf("%d.%09d", whole, fraction))-trailingZeroes(fraction)]
}

func trailingZeroes(fraction int64) int {
	count := 0
	for count < 9 && fraction%10 == 0 {
		fraction /= 10
		count++
	}
	return count
}

func Add(left, right NanoUSD) (NanoUSD, error) {
	if right > 0 && left > NanoUSD(math.MaxInt64)-right {
		return 0, ErrOverflow
	}
	if right < 0 && left < NanoUSD(math.MinInt64)-right {
		return 0, ErrOverflow
	}
	return left + right, nil
}

func Sub(left, right NanoUSD) (NanoUSD, error) {
	if right == NanoUSD(math.MinInt64) {
		return 0, ErrOverflow
	}
	return Add(left, -right)
}

// CostForTokens returns ceil(tokens * rate-per-million / 1_000_000).
// Ceiling is intentional: no non-zero billable usage rounds down to free.
func CostForTokens(tokens int64, ratePerMillion NanoUSD) (NanoUSD, error) {
	return CostForTokensScaled(tokens, ratePerMillion, 1, 1)
}

// CostForTokensScaled returns ceil(tokens * rate * num / (1_000_000 * den)).
//
// The scale is an exact rational so time-of-day pricing ("peak costs 2x")
// composes over a base rate without floating point. Critically the scale is
// folded into the SAME ceiling as the token division rather than being applied
// to the rate first: scaling a rate and then costing it would round twice, and
// the second rounding would silently overcharge on every request.
func CostForTokensScaled(tokens int64, ratePerMillion NanoUSD, num, den int64) (NanoUSD, error) {
	if num < 0 {
		return 0, ErrNegative
	}
	if den <= 0 {
		return 0, ErrInvalid
	}
	if tokens < 0 || ratePerMillion < 0 {
		return 0, ErrNegative
	}
	if tokens == 0 || ratePerMillion == 0 || num == 0 {
		return 0, nil
	}
	numerator := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(int64(ratePerMillion)))
	numerator.Mul(numerator, big.NewInt(num))
	denominator := new(big.Int).Mul(big.NewInt(PerMillion), big.NewInt(den))
	return ceilQuotient(numerator, denominator)
}

// MulDivCeil returns ceil(units * rate / divisor) with checked intermediate
// arithmetic. It is shared by token and duration pricing.
func MulDivCeil(units int64, rate NanoUSD, divisor int64) (NanoUSD, error) {
	if units < 0 || rate < 0 {
		return 0, ErrNegative
	}
	if divisor <= 0 {
		return 0, ErrInvalid
	}
	if units == 0 || rate == 0 {
		return 0, nil
	}
	numerator := new(big.Int).Mul(big.NewInt(units), big.NewInt(int64(rate)))
	return ceilQuotient(numerator, big.NewInt(divisor))
}

// ceilQuotient returns ceil(numerator/denominator) for non-negative inputs,
// erroring when the result does not fit in NanoUSD. It is the single rounding
// point shared by every pricing path.
func ceilQuotient(numerator, denominator *big.Int) (NanoUSD, error) {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, ErrOverflow
	}
	return NanoUSD(quotient.Int64()), nil
}

func Min(left, right NanoUSD) NanoUSD {
	if left < right {
		return left
	}
	return right
}

func Max(left, right NanoUSD) NanoUSD {
	if left > right {
		return left
	}
	return right
}
