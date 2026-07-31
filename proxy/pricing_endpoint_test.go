package proxy

import (
	"testing"
	"time"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

func TestPricingSnapshotReportsEffectiveRatesAndWindow(t *testing.T) {
	model := openAIModel()
	model.PriceWindows = []pricing.Window{{
		ID: "peak", Label: "Peak", StartMinuteUTC: 12 * 60, EndMinuteUTC: 14 * 60,
		WeekdayMask: 127, MultiplierNum: 2, MultiplierDen: 1,
	}}

	inPeak := time.Date(2026, 7, 31, 13, 0, 0, 0, time.UTC)
	view, err := modelPricingSnapshot(model, inPeak)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveWindow == nil || view.ActiveWindow.ID != "peak" {
		t.Fatalf("active window = %+v", view.ActiveWindow)
	}
	if view.ActiveWindow.Multiplier != 2 {
		t.Fatalf("multiplier = %v", view.ActiveWindow.Multiplier)
	}
	// The published rate must already reflect the active window: $3.00 doubled.
	var input pricingRate
	for _, rate := range view.EffectiveRates {
		if rate.Class == pricing.ClassInputFresh {
			input = rate
		}
	}
	if input.NanoUSDPerM != money.NanoUSD(6_000_000_000) {
		t.Fatalf("input rate = %d, want the doubled 6000000000", input.NanoUSDPerM)
	}
	if input.USDPerMillion != 6 {
		t.Fatalf("usd per million = %v, want 6", input.USDPerMillion)
	}

	if view.NextChangeAt == nil {
		t.Fatal("expected a next-change time inside a bounded window")
	}
	if !view.NextChangeAt.Equal(time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("next change = %s, want 14:00", view.NextChangeAt)
	}
	if view.NextChangeInS == nil || *view.NextChangeInS != 3600 {
		t.Fatalf("next change in seconds = %v, want 3600", view.NextChangeInS)
	}
}

func TestPricingSnapshotOutsideWindow(t *testing.T) {
	model := openAIModel()
	model.PriceWindows = []pricing.Window{{
		ID: "peak", StartMinuteUTC: 12 * 60, EndMinuteUTC: 14 * 60,
		WeekdayMask: 127, MultiplierNum: 2, MultiplierDen: 1,
	}}
	view, err := modelPricingSnapshot(model, time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveWindow != nil {
		t.Fatalf("expected no active window, got %+v", view.ActiveWindow)
	}
	if view.UpcomingWindow == nil || view.UpcomingWindow.ID != "peak" {
		t.Fatalf("upcoming window = %+v", view.UpcomingWindow)
	}
}

// A model with no windows has no next change; reporting one would imply a
// price movement that will never happen.
func TestPricingSnapshotWithoutWindowsHasNoNextChange(t *testing.T) {
	view, err := modelPricingSnapshot(openAIModel(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if view.NextChangeAt != nil {
		t.Fatalf("unexpected next change %s", view.NextChangeAt)
	}
	if view.ActiveWindow != nil {
		t.Fatal("unexpected active window")
	}
}

// Inherited rates must be marked, so an operator can tell a deliberate price
// from a fallback when reading the catalogue.
func TestPricingSnapshotMarksInheritedRates(t *testing.T) {
	model := openAIModel()
	rate := money.NanoUSD(1_250_000_000)
	model.CacheTTLRates = []pricing.TTLRate{{
		TTL: pricing.TTL5m, CacheWritePerMillion: &rate,
	}}
	view, err := modelPricingSnapshot(model, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	byClass := map[pricing.TokenClass]pricingRate{}
	for _, entry := range view.EffectiveRates {
		byClass[entry.Class] = entry
	}
	if byClass[pricing.ClassCacheWrite5m].IsInherited {
		t.Fatal("an explicitly configured 5m write rate must not be marked inherited")
	}
	if byClass[pricing.ClassCacheWrite5m].NanoUSDPerM != rate {
		t.Fatalf("5m write rate = %d, want %d", byClass[pricing.ClassCacheWrite5m].NanoUSDPerM, rate)
	}
	if !byClass[pricing.ClassCacheWrite1h].IsInherited {
		t.Fatal("an unset 1h write rate must be marked inherited")
	}
}

func TestPricingSnapshotIncludesTiers(t *testing.T) {
	model := openAIModel()
	model.PricingTiers = []pricing.Tier{{
		MinInputTokensExclusive: 272_000,
		Rates:                   pricing.Rates{InputPerMillion: 6_000_000_000, CacheWritePerMillion: 2_500_000_000},
	}}
	view, err := modelPricingSnapshot(model, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tiers) != 1 {
		t.Fatalf("tiers = %d, want 1", len(view.Tiers))
	}
	if view.Tiers[0].MinPromptTokensExclusive != 272_000 {
		t.Fatalf("threshold = %d", view.Tiers[0].MinPromptTokensExclusive)
	}
}

func TestPricingSnapshotRejectsUnpriceableModel(t *testing.T) {
	// A negative rate cannot produce a valid rule set; the endpoint must skip
	// such a model rather than serving a nonsense price.
	broken := &db.Model{ID: "broken", Name: "broken", InputCostNanoPerMillion: -1}
	if _, err := modelPricingSnapshot(broken, time.Now().UTC()); err == nil {
		t.Fatal("expected an error for a model with a negative rate")
	}
}
