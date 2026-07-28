// Package pricing evaluates immutable, exact token-price snapshots.
//
// A request is priced from one RuleSet chosen before admission. The same
// snapshot must be used for reservation and settlement; callers must not reload
// mutable model prices between those two operations.
package pricing

import (
	"fmt"
	"sort"

	"gateway/money"
)

type Rates struct {
	InputPerMillion      money.NanoUSD `json:"input_nano_usd_per_million"`
	OutputPerMillion     money.NanoUSD `json:"output_nano_usd_per_million"`
	CacheReadPerMillion  money.NanoUSD `json:"cache_read_nano_usd_per_million"`
	CacheWritePerMillion money.NanoUSD `json:"cache_write_nano_usd_per_million"`
}

type Tier struct {
	// MinInputTokensExclusive selects this tier only when the request's total
	// input token count is greater than this value.
	MinInputTokensExclusive int64 `json:"min_input_tokens_exclusive"`
	Rates                   Rates `json:"rates"`
}

type RuleSet struct {
	ID        string
	BaseRates Rates
	Tiers     []Tier
}

type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

type Quote struct {
	RuleSetID string
	Rates     Rates
	Usage     Usage
	Cost      money.NanoUSD
}

func (r RuleSet) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("pricing rule set id is required")
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
	return nil
}

func validateRates(r Rates) error {
	if r.InputPerMillion < 0 || r.OutputPerMillion < 0 ||
		r.CacheReadPerMillion < 0 || r.CacheWritePerMillion < 0 {
		return money.ErrNegative
	}
	return nil
}

// NewRuleSet makes a defensive copy and sorts tiers into deterministic order.
func NewRuleSet(id string, base Rates, tiers []Tier) (RuleSet, error) {
	copied := append([]Tier(nil), tiers...)
	sort.Slice(copied, func(i, j int) bool {
		return copied[i].MinInputTokensExclusive < copied[j].MinInputTokensExclusive
	})
	rules := RuleSet{ID: id, BaseRates: base, Tiers: copied}
	if err := rules.Validate(); err != nil {
		return RuleSet{}, err
	}
	return rules, nil
}

func (r RuleSet) RatesForInput(inputTokens int64) Rates {
	selected := r.BaseRates
	for _, tier := range r.Tiers {
		if inputTokens <= tier.MinInputTokensExclusive {
			break
		}
		selected = tier.Rates
	}
	return selected
}

func (r RuleSet) Quote(usage Usage) (Quote, error) {
	if err := r.Validate(); err != nil {
		return Quote{}, err
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 {
		return Quote{}, money.ErrNegative
	}

	rates := r.RatesForInput(usage.InputTokens)
	uncachedInput := usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	writeRate := rates.CacheWritePerMillion
	if writeRate == 0 && usage.CacheWriteTokens > 0 {
		writeRate = rates.InputPerMillion
	}

	components := []struct {
		tokens int64
		rate   money.NanoUSD
	}{
		{uncachedInput, rates.InputPerMillion},
		{usage.OutputTokens, rates.OutputPerMillion},
		{usage.CacheReadTokens, rates.CacheReadPerMillion},
		{usage.CacheWriteTokens, writeRate},
	}

	var total money.NanoUSD
	for _, component := range components {
		cost, err := money.CostForTokens(component.tokens, component.rate)
		if err != nil {
			return Quote{}, err
		}
		total, err = money.Add(total, cost)
		if err != nil {
			return Quote{}, err
		}
	}
	return Quote{RuleSetID: r.ID, Rates: rates, Usage: usage, Cost: total}, nil
}

// MaxOutputTokens returns the largest output-token allowance whose quote does
// not exceed budget. It uses binary search because tier choice depends only on
// input tokens, making quote cost monotonic in output tokens.
func (r RuleSet) MaxOutputTokens(input Usage, requested int64, budget money.NanoUSD) (int64, error) {
	if requested < 0 || budget < 0 {
		return 0, money.ErrNegative
	}
	input.OutputTokens = 0
	base, err := r.Quote(input)
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
		quote, err := r.Quote(input)
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
