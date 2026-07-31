// Package pricing evaluates immutable, exact token-price snapshots.
//
// A request is priced from one RuleSet chosen before admission. The same
// snapshot must be used for admission and settlement; callers must not reload
// mutable model prices between those two operations.
//
// Rates resolve in four ordered layers:
//
//	L1 base rates          -> the model's own prices
//	L2 context tier        -> absolute override selected by total prompt size
//	L3 cache TTL variant   -> absolute per-class override (5m / 1h)
//	L4 time-of-day window  -> exact rational multiplier over the result
//
// L2 and L3 are absolute because providers publish them that way ("above 272K
// tokens, cache writes cost $2.50/M"). L4 is a multiplier because peak pricing
// is published as a factor ("2x during peak hours"), so one row expresses it
// for every tier and TTL at once.
package pricing

import (
	"fmt"
	"sort"
	"time"

	"gateway/money"
)

type Rates struct {
	InputPerMillion      money.NanoUSD `json:"input_nano_usd_per_million"`
	OutputPerMillion     money.NanoUSD `json:"output_nano_usd_per_million"`
	CacheReadPerMillion  money.NanoUSD `json:"cache_read_nano_usd_per_million"`
	CacheWritePerMillion money.NanoUSD `json:"cache_write_nano_usd_per_million"`

	// TTL-specific cache rates. Zero means "inherit the default-TTL rate
	// above", which is what keeps every model that predates TTL pricing
	// costing exactly what it costed before.
	CacheRead5mPerMillion  money.NanoUSD `json:"cache_read_5m_nano_usd_per_million,omitempty"`
	CacheWrite5mPerMillion money.NanoUSD `json:"cache_write_5m_nano_usd_per_million,omitempty"`
	CacheWrite1hPerMillion money.NanoUSD `json:"cache_write_1h_nano_usd_per_million,omitempty"`
}

// RateFor resolves the per-million rate for one token class, applying the
// inheritance chain: TTL variant -> default TTL -> input rate.
func (r Rates) RateFor(class TokenClass) money.NanoUSD {
	switch class {
	case ClassInputFresh:
		return r.InputPerMillion
	case ClassOutput:
		return r.OutputPerMillion
	case ClassCacheRead:
		return r.CacheReadPerMillion
	case ClassCacheRead5m:
		return firstNonZero(r.CacheRead5mPerMillion, r.CacheReadPerMillion)
	case ClassCacheWrite:
		return r.effectiveCacheWrite()
	case ClassCacheWrite5m:
		return firstNonZero(r.CacheWrite5mPerMillion, r.effectiveCacheWrite())
	case ClassCacheWrite1h:
		return firstNonZero(r.CacheWrite1hPerMillion, r.effectiveCacheWrite())
	default:
		return 0
	}
}

// effectiveCacheWrite preserves the long-standing rule that a model with no
// configured cache-write price bills cache writes at its input rate, rather
// than giving them away for free.
func (r Rates) effectiveCacheWrite() money.NanoUSD {
	return firstNonZero(r.CacheWritePerMillion, r.InputPerMillion)
}

func firstNonZero(values ...money.NanoUSD) money.NanoUSD {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

type Tier struct {
	// MinInputTokensExclusive selects this tier only when the request's total
	// prompt token count is greater than this value.
	MinInputTokensExclusive int64 `json:"min_input_tokens_exclusive"`
	Rates                   Rates `json:"rates"`
}

// BaseTierThreshold is the receipt's tier marker when no tier applied.
const BaseTierThreshold int64 = -1

type RuleSet struct {
	ID         string
	BaseRates  Rates
	Tiers      []Tier
	Windows    []Window
	Accounting PromptAccounting
}

// Spec describes a rule set before validation.
type Spec struct {
	ID         string
	Base       Rates
	Tiers      []Tier
	Windows    []Window
	Accounting PromptAccounting
}

// PriceLine is one token class's contribution to a charge.
type PriceLine struct {
	Class TokenClass `json:"class"`
	Tokens int64     `json:"tokens"`
	// RatePerMillion is the rate BEFORE the time-window multiplier, so a
	// receipt shows both the catalogue price and what was actually charged.
	RatePerMillion money.NanoUSD `json:"rate_nano_usd_per_million"`
	Cost           money.NanoUSD `json:"cost_nano_usd"`
}

// Receipt is the persisted derivation of a charge. Given a receipt alone, the
// cost can be re-derived without the models table — which is what makes a
// charge auditable rather than merely recorded.
type Receipt struct {
	RuleSetID     string           `json:"rule_set_id"`
	PricedAt      time.Time        `json:"priced_at"`
	TierThreshold int64            `json:"tier_threshold"`
	WindowID      string           `json:"window_id,omitempty"`
	WindowLabel   string           `json:"window_label,omitempty"`
	MultiplierNum int64            `json:"multiplier_num"`
	MultiplierDen int64            `json:"multiplier_den"`
	Accounting    PromptAccounting `json:"prompt_accounting"`
	Lines         []PriceLine      `json:"lines"`
	Total         money.NanoUSD    `json:"total_nano_usd"`
}

type Quote struct {
	RuleSetID string
	Rates     Rates
	Usage     Usage
	Cost      money.NanoUSD
	Receipt   Receipt
}

func (r RuleSet) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("pricing rule set id is required")
	}
	if r.Accounting != "" && !r.Accounting.Valid() {
		return fmt.Errorf("unknown prompt accounting mode %q", r.Accounting)
	}
	if err := validateRates(r.BaseRates); err != nil {
		return fmt.Errorf("base rates: %w", err)
	}
	previous := int64(-1)
	for i, tier := range r.Tiers {
		if tier.MinInputTokensExclusive < 0 {
			return fmt.Errorf("tier %d threshold cannot be negative", i)
		}
		if tier.MinInputTokensExclusive <= previous {
			return fmt.Errorf("tiers must be strictly ordered by input threshold")
		}
		if err := validateRates(tier.Rates); err != nil {
			return fmt.Errorf("tier %d: %w", i, err)
		}
		previous = tier.MinInputTokensExclusive
	}
	seen := make(map[string]struct{}, len(r.Windows))
	for _, window := range r.Windows {
		if err := window.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[window.ID]; duplicate {
			return fmt.Errorf("duplicate price window id %q", window.ID)
		}
		seen[window.ID] = struct{}{}
	}
	return nil
}

func validateRates(r Rates) error {
	if r.InputPerMillion < 0 || r.OutputPerMillion < 0 ||
		r.CacheReadPerMillion < 0 || r.CacheWritePerMillion < 0 ||
		r.CacheRead5mPerMillion < 0 || r.CacheWrite5mPerMillion < 0 ||
		r.CacheWrite1hPerMillion < 0 {
		return money.ErrNegative
	}
	return nil
}

// New builds a validated rule set, making defensive copies and sorting both
// tiers and windows into deterministic order.
func New(spec Spec) (RuleSet, error) {
	tiers := append([]Tier(nil), spec.Tiers...)
	sort.Slice(tiers, func(i, j int) bool {
		return tiers[i].MinInputTokensExclusive < tiers[j].MinInputTokensExclusive
	})
	windows := append([]Window(nil), spec.Windows...)
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Priority != windows[j].Priority {
			return windows[i].Priority > windows[j].Priority
		}
		return windows[i].ID < windows[j].ID
	})
	accounting := spec.Accounting
	if accounting == "" {
		accounting = PromptInclusive
	}
	rules := RuleSet{
		ID:         spec.ID,
		BaseRates:  spec.Base,
		Tiers:      tiers,
		Windows:    windows,
		Accounting: accounting,
	}
	if err := rules.Validate(); err != nil {
		return RuleSet{}, err
	}
	return rules, nil
}

// NewRuleSet builds a rule set with no time windows.
func NewRuleSet(id string, base Rates, tiers []Tier) (RuleSet, error) {
	return New(Spec{ID: id, Base: base, Tiers: tiers})
}

// RatesForPromptTotal selects the context tier for a prompt size. The total
// prompt (including cached tokens) is the correct input here: a provider's
// "above 272K tokens" price refers to how much context it had to handle, not
// to how much of it happened to be uncached.
func (r RuleSet) RatesForPromptTotal(promptTokens int64) (Rates, int64) {
	selected := r.BaseRates
	threshold := BaseTierThreshold
	for _, tier := range r.Tiers {
		if promptTokens <= tier.MinInputTokensExclusive {
			break
		}
		selected = tier.Rates
		threshold = tier.MinInputTokensExclusive
	}
	return selected, threshold
}

// Quote prices usage with no time-of-day window applied.
func (r RuleSet) Quote(usage Usage) (Quote, error) {
	return r.QuoteAt(usage, time.Time{})
}

// QuoteAt prices usage as of a pinned instant.
//
// The instant is passed in rather than read from the clock so that admission
// and settlement of the same request always agree: a request quoted just
// before a peak-pricing boundary is billed at the rate it was quoted, even
// when it settles on the far side of that boundary.
func (r RuleSet) QuoteAt(usage Usage, at time.Time) (Quote, error) {
	if err := r.Validate(); err != nil {
		return Quote{}, err
	}
	if usage.negative() {
		return Quote{}, money.ErrNegative
	}

	rates, threshold := r.RatesForPromptTotal(usage.PromptTotalTokens)
	window := r.WindowFor(at)

	receipt := Receipt{
		RuleSetID:     r.ID,
		PricedAt:      at,
		TierThreshold: threshold,
		MultiplierNum: 1,
		MultiplierDen: 1,
		Accounting:    r.Accounting,
	}
	if window != nil {
		receipt.WindowID = window.ID
		receipt.WindowLabel = window.Label
		receipt.MultiplierNum = window.MultiplierNum
		receipt.MultiplierDen = window.MultiplierDen
	}

	var total money.NanoUSD
	for _, class := range TokenClasses {
		tokens := usage.TokensFor(class)
		if tokens == 0 {
			continue
		}
		rate := rates.RateFor(class)
		num, den := int64(1), int64(1)
		if window != nil {
			num, den = window.scaleFor(class)
		}
		cost, err := money.CostForTokensScaled(tokens, rate, num, den)
		if err != nil {
			return Quote{}, err
		}
		total, err = money.Add(total, cost)
		if err != nil {
			return Quote{}, err
		}
		receipt.Lines = append(receipt.Lines, PriceLine{
			Class:          class,
			Tokens:         tokens,
			RatePerMillion: rate,
			Cost:           cost,
		})
	}
	receipt.Total = total

	return Quote{
		RuleSetID: r.ID,
		Rates:     rates,
		Usage:     usage,
		Cost:      total,
		Receipt:   receipt,
	}, nil
}

// MaxOutputTokens returns the largest output-token allowance affordable within
// budget, with no time window applied.
func (r RuleSet) MaxOutputTokens(input Usage, requested int64, budget money.NanoUSD) (int64, error) {
	return r.MaxOutputTokensAt(input, requested, budget, time.Time{})
}

// MaxOutputTokensAt returns the largest output-token allowance whose quote does
// not exceed budget at a pinned instant.
//
// Binary search is valid because cost is monotonic in output tokens: the tier
// depends only on prompt size and the window only on the pinned instant, so
// neither can change as the output allowance varies.
func (r RuleSet) MaxOutputTokensAt(input Usage, requested int64, budget money.NanoUSD, at time.Time) (int64, error) {
	if requested < 0 || budget < 0 {
		return 0, money.ErrNegative
	}
	input.OutputTokens = 0
	base, err := r.QuoteAt(input, at)
	if err != nil {
		return 0, err
	}
	if base.Cost > budget {
		return 0, nil
	}
	low, high := int64(0), requested
	for low < high {
		mid := low + (high-low+1)/2
		input.OutputTokens = mid
		quote, err := r.QuoteAt(input, at)
		if err != nil {
			return 0, err
		}
		if quote.Cost <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low, nil
}
