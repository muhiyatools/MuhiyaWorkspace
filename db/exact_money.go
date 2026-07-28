package db

import (
	"fmt"

	"gateway/money"
)

// normalize*Money functions are the only legacy float conversion boundary
// during migration 023. Exact values win when both representations are set.

func normalizeBudgetWindowMoney(window *BudgetWindow) error {
	if window.BudgetNanoUSD < 0 || window.BudgetUSD < 0 {
		return money.ErrNegative
	}
	if window.BudgetNanoUSD == 0 && window.BudgetUSD != 0 {
		exact, err := money.FromUSD(window.BudgetUSD)
		if err != nil {
			return fmt.Errorf("budget: %w", err)
		}
		window.BudgetNanoUSD = exact
	}
	window.BudgetUSD = window.BudgetNanoUSD.USD()
	return nil
}

func normalizeModelMoney(model *Model) error {
	pairs := []struct {
		name   string
		legacy *float64
		exact  *money.NanoUSD
	}{
		{"input price", &model.InputCostPerMillion, &model.InputCostNanoPerMillion},
		{"output price", &model.OutputCostPerMillion, &model.OutputCostNanoPerMillion},
		{"cache-read price", &model.CacheReadCostPerMillion, &model.CacheReadCostNanoPerMillion},
		{"cache-write price", &model.CacheWriteCostPerMillion, &model.CacheWriteCostNanoPerMillion},
		{"per-minute price", &model.PricePerMinute, &model.PricePerMinuteNano},
	}
	for _, pair := range pairs {
		if *pair.exact < 0 || *pair.legacy < 0 {
			return fmt.Errorf("%s: %w", pair.name, money.ErrNegative)
		}
		if *pair.exact == 0 && *pair.legacy != 0 {
			exact, err := money.FromUSD(*pair.legacy)
			if err != nil {
				return fmt.Errorf("%s: %w", pair.name, err)
			}
			*pair.exact = exact
		}
		*pair.legacy = pair.exact.USD()
	}
	return nil
}

func normalizeRequestLogMoney(entry *RequestLog) error {
	if entry.CostNanoUSD < 0 || entry.Cost < 0 {
		return money.ErrNegative
	}
	if entry.CostNanoUSD == 0 && entry.Cost != 0 {
		exact, err := money.FromUSD(entry.Cost)
		if err != nil {
			return fmt.Errorf("request cost: %w", err)
		}
		entry.CostNanoUSD = exact
	}
	entry.Cost = entry.CostNanoUSD.USD()
	return nil
}

func normalizeTopupMoney(topup *UserTopup) error {
	if topup.AmountNanoUSD < 0 || topup.UsedNanoUSD < 0 || topup.Credits < 0 || topup.UsedCredits < 0 {
		return money.ErrNegative
	}
	if topup.AmountNanoUSD == 0 && topup.Credits != 0 {
		exact, err := money.FromCredits(topup.Credits)
		if err != nil {
			return fmt.Errorf("top-up amount: %w", err)
		}
		topup.AmountNanoUSD = exact
	}
	if topup.UsedNanoUSD == 0 && topup.UsedCredits != 0 {
		exact, err := money.FromCredits(topup.UsedCredits)
		if err != nil {
			return fmt.Errorf("top-up used amount: %w", err)
		}
		topup.UsedNanoUSD = exact
	}
	if topup.UsedNanoUSD > topup.AmountNanoUSD {
		return fmt.Errorf("top-up used amount exceeds total")
	}
	topup.Credits = topup.AmountNanoUSD.Credits()
	topup.UsedCredits = topup.UsedNanoUSD.Credits()
	return nil
}
