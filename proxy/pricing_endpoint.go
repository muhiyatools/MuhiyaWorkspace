package proxy

import (
	"encoding/json"
	"net/http"
	"time"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

// pricingRate is one token class's effective price, expressed both as exact
// nano-USD and as the per-million-token USD figure providers publish, so a
// caller can display it without re-deriving anything.
type pricingRate struct {
	Class          pricing.TokenClass `json:"class"`
	NanoUSDPerM    money.NanoUSD      `json:"nano_usd_per_million"`
	USDPerMillion  float64            `json:"usd_per_million"`
	IsInherited    bool               `json:"inherited,omitempty"`
}

type pricingWindowView struct {
	ID            string   `json:"id"`
	Label         string   `json:"label,omitempty"`
	MultiplierNum int64    `json:"multiplier_num"`
	MultiplierDen int64    `json:"multiplier_den"`
	Multiplier    float64  `json:"multiplier"`
	AppliesTo     []string `json:"applies_to,omitempty"`
}

type pricingTierView struct {
	MinPromptTokensExclusive int64         `json:"min_prompt_tokens_exclusive"`
	Rates                    []pricingRate `json:"rates"`
}

type modelPricingView struct {
	Model            string             `json:"model"`
	DisplayName      string             `json:"display_name,omitempty"`
	ContextWindow    int                `json:"context_window,omitempty"`
	PromptAccounting string             `json:"prompt_accounting"`
	RuleSetID        string             `json:"rule_set_id"`
	EffectiveRates   []pricingRate      `json:"effective_rates"`
	ActiveWindow     *pricingWindowView `json:"active_window,omitempty"`
	// NextChangeAt is when the effective price next changes, so a caller can
	// tell a user how long the current rate lasts — the practical point of
	// peak/off-peak pricing is deciding whether to run a large job now.
	NextChangeAt   *time.Time         `json:"next_change_at,omitempty"`
	NextChangeInS  *int64             `json:"next_change_in_seconds,omitempty"`
	Tiers          []pricingTierView  `json:"tiers,omitempty"`
	UpcomingWindow *pricingWindowView `json:"upcoming_window,omitempty"`
}

// handlePricing serves the effective price of every model this key can see,
// as of now, including which time-of-day window is in force.
func (h *ProxyHandler) handlePricing(w http.ResponseWriter, r *http.Request) {
	onlyMuhiyaCodeVisible := getClientAppName(r) == "MuhiyaCode"
	models, err := h.db.ListModels()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to load models", "api_error")
		return
	}

	now := time.Now().UTC()
	views := make([]modelPricingView, 0, len(models))
	for i := range models {
		model := &models[i]
		if !discoverableModel(model, onlyMuhiyaCodeVisible) {
			continue
		}
		view, err := modelPricingSnapshot(model, now)
		if err != nil {
			// One misconfigured model must not blank the whole price list.
			continue
		}
		views = append(views, view)
	}

	w.Header().Set("Content-Type", "application/json")
	// Never cache: the whole point of this endpoint is that the answer changes
	// at a window boundary, and a cached copy would show a stale price.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object":   "list",
		"as_of":    now.Format(time.RFC3339),
		"currency": "USD",
		"data":     views,
	})
}

func modelPricingSnapshot(model *db.Model, now time.Time) (modelPricingView, error) {
	rules, err := pricingRulesForModel(model)
	if err != nil {
		return modelPricingView{}, err
	}
	baseRates, _ := rules.RatesForPromptTotal(0)
	window := rules.WindowFor(now)

	view := modelPricingView{
		Model:            model.Name,
		DisplayName:      model.DisplayName,
		ContextWindow:    model.ContextWindow,
		PromptAccounting: string(rules.Accounting),
		RuleSetID:        rules.ID,
		EffectiveRates:   ratesView(baseRates, window),
	}
	if window != nil {
		view.ActiveWindow = windowView(window)
	}
	if next := rules.NextBoundaryAfter(now); !next.IsZero() {
		at := next
		seconds := int64(next.Sub(now).Seconds())
		view.NextChangeAt = &at
		view.NextChangeInS = &seconds
		if upcoming := rules.WindowFor(next); upcoming != nil {
			view.UpcomingWindow = windowView(upcoming)
		}
	}
	for _, tier := range rules.Tiers {
		view.Tiers = append(view.Tiers, pricingTierView{
			MinPromptTokensExclusive: tier.MinInputTokensExclusive,
			Rates:                    ratesView(tier.Rates, window),
		})
	}
	return view, nil
}

// ratesView renders every token class at its effective price, marking the ones
// that are inherited rather than configured so a reader can tell a deliberate
// price from a fallback.
func ratesView(rates pricing.Rates, window *pricing.Window) []pricingRate {
	out := make([]pricingRate, 0, len(pricing.TokenClasses))
	for _, class := range pricing.TokenClasses {
		rate := rates.RateFor(class)
		scaled := rate
		if window != nil {
			// Scale one million tokens so the displayed per-million rate
			// already reflects the active window.
			if cost, err := scaledRatePerMillion(rate, class, window); err == nil {
				scaled = cost
			}
		}
		out = append(out, pricingRate{
			Class:         class,
			NanoUSDPerM:   scaled,
			USDPerMillion: scaled.USD(),
			IsInherited:   inheritedClass(rates, class),
		})
	}
	return out
}

func scaledRatePerMillion(rate money.NanoUSD, class pricing.TokenClass, window *pricing.Window) (money.NanoUSD, error) {
	num, den := window.MultiplierNum, window.MultiplierDen
	if len(window.AppliesTo) > 0 {
		num, den = 1, 1
		for _, applies := range window.AppliesTo {
			if applies == class {
				num, den = window.MultiplierNum, window.MultiplierDen
				break
			}
		}
	}
	return money.CostForTokensScaled(money.PerMillion, rate, num, den)
}

// inheritedClass reports whether a class has no rate of its own and is falling
// back to another class's price.
func inheritedClass(rates pricing.Rates, class pricing.TokenClass) bool {
	switch class {
	case pricing.ClassCacheRead5m:
		return rates.CacheRead5mPerMillion == 0
	case pricing.ClassCacheWrite5m:
		return rates.CacheWrite5mPerMillion == 0
	case pricing.ClassCacheWrite1h:
		return rates.CacheWrite1hPerMillion == 0
	case pricing.ClassCacheWrite:
		return rates.CacheWritePerMillion == 0
	default:
		return false
	}
}

func windowView(window *pricing.Window) *pricingWindowView {
	view := &pricingWindowView{
		ID:            window.ID,
		Label:         window.Label,
		MultiplierNum: window.MultiplierNum,
		MultiplierDen: window.MultiplierDen,
	}
	if window.MultiplierDen != 0 {
		view.Multiplier = float64(window.MultiplierNum) / float64(window.MultiplierDen)
	}
	for _, class := range window.AppliesTo {
		view.AppliesTo = append(view.AppliesTo, string(class))
	}
	return view
}
