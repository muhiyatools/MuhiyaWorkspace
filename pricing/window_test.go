package pricing

import (
	"testing"
	"time"

	"gateway/money"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func peakRuleSet(t *testing.T, windows ...Window) RuleSet {
	t.Helper()
	rules, err := New(Spec{
		ID:      "peak",
		Base:    Rates{InputPerMillion: 300_000_000, OutputPerMillion: 1_200_000_000},
		Windows: windows,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// The headline scenario: a provider that doubles its price during peak hours.
func TestPeakWindowDoublesCostExactly(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "peak", Label: "Peak",
		StartMinuteUTC: 30, EndMinuteUTC: 16*60 + 30,
		WeekdayMask:   allWeekdays,
		MultiplierNum: 2, MultiplierDen: 1,
	})
	usage := InputUsage(1_000_000)
	usage.OutputTokens = 500_000

	offPeak, err := rules.QuoteAt(usage, at(t, "2026-07-31T20:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	peak, err := rules.QuoteAt(usage, at(t, "2026-07-31T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if peak.Cost != offPeak.Cost*2 {
		t.Fatalf("peak %d is not exactly double off-peak %d", peak.Cost, offPeak.Cost)
	}
	if peak.Receipt.WindowID != "peak" {
		t.Fatalf("peak receipt window = %q", peak.Receipt.WindowID)
	}
	if offPeak.Receipt.WindowID != "" {
		t.Fatalf("off-peak should record no window, got %q", offPeak.Receipt.WindowID)
	}
	if peak.Receipt.MultiplierNum != 2 || peak.Receipt.MultiplierDen != 1 {
		t.Fatalf("receipt multiplier = %d/%d", peak.Receipt.MultiplierNum, peak.Receipt.MultiplierDen)
	}
}

// DeepSeek publishes off-peak hours that run from the evening past midnight,
// so a window whose start is later than its end must wrap the day boundary.
func TestWindowWrappingMidnight(t *testing.T) {
	window := Window{
		ID:             "off-peak",
		StartMinuteUTC: 16*60 + 30, // 16:30
		EndMinuteUTC:   30,         // 00:30
		WeekdayMask:    allWeekdays,
		MultiplierNum:  1, MultiplierDen: 2,
	}
	cases := map[string]bool{
		"2026-07-31T23:00:00Z": true,  // evening segment
		"2026-07-31T16:30:00Z": true,  // inclusive start
		"2026-07-31T00:15:00Z": true,  // early-morning segment
		"2026-07-31T00:30:00Z": false, // exclusive end
		"2026-07-31T12:00:00Z": false, // the middle of the day
		"2026-07-31T16:29:00Z": false, // one minute before start
	}
	for value, want := range cases {
		if got := window.Matches(at(t, value)); got != want {
			t.Fatalf("Matches(%s) = %v, want %v", value, got, want)
		}
	}
}

func TestWindowWeekdayMask(t *testing.T) {
	// Monday only: ISO Monday is bit 0.
	window := Window{
		ID: "weekday", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
		WeekdayMask: 1, MultiplierNum: 2, MultiplierDen: 1,
	}
	// 2026-07-27 is a Monday; 2026-07-28 a Tuesday.
	if !window.Matches(at(t, "2026-07-27T10:00:00Z")) {
		t.Fatal("Monday should match")
	}
	if window.Matches(at(t, "2026-07-28T10:00:00Z")) {
		t.Fatal("Tuesday should not match")
	}
}

// Windows must not stack. Two overlapping rows means the higher priority wins
// outright, because compounding multipliers would make a bill impossible to
// explain and one bad row could quietly quadruple a charge.
func TestOverlappingWindowsPickHighestPriorityOnly(t *testing.T) {
	rules := peakRuleSet(t,
		Window{ID: "low", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
			WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1, Priority: 1},
		Window{ID: "high", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
			WeekdayMask: allWeekdays, MultiplierNum: 3, MultiplierDen: 1, Priority: 5},
	)
	quote, err := rules.QuoteAt(InputUsage(1_000_000), at(t, "2026-07-31T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if quote.Receipt.WindowID != "high" {
		t.Fatalf("window = %q, want the higher priority row", quote.Receipt.WindowID)
	}
	// 3x, not 6x.
	if quote.Cost != 900_000_000 {
		t.Fatalf("cost = %d, want 900000000 (3x, not compounded)", quote.Cost)
	}
}

func TestWindowAppliesToSubsetOfClasses(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "output-only", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
		WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1,
		AppliesTo: []TokenClass{ClassOutput},
	})
	usage := InputUsage(1_000_000)
	usage.OutputTokens = 1_000_000
	quote, err := rules.QuoteAt(usage, at(t, "2026-07-31T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	// Input unscaled at .30, output doubled from 1.20 to 2.40.
	if quote.Cost != 300_000_000+2_400_000_000 {
		t.Fatalf("cost = %d", quote.Cost)
	}
}

func TestWindowEffectiveRange(t *testing.T) {
	from := at(t, "2026-08-01T00:00:00Z")
	until := at(t, "2026-09-01T00:00:00Z")
	window := Window{
		ID: "promo", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
		WeekdayMask: allWeekdays, MultiplierNum: 1, MultiplierDen: 2,
		EffectiveFrom: &from, EffectiveUntil: &until,
	}
	if window.Matches(at(t, "2026-07-31T12:00:00Z")) {
		t.Fatal("before effective_from should not match")
	}
	if !window.Matches(at(t, "2026-08-15T12:00:00Z")) {
		t.Fatal("inside the effective range should match")
	}
	if window.Matches(at(t, "2026-09-01T00:00:00Z")) {
		t.Fatal("effective_until is exclusive")
	}
}

// A zero instant means "no time pinned", which must not silently pick a window.
func TestZeroInstantAppliesNoWindow(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "always", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
		WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1,
	})
	quote, err := rules.Quote(InputUsage(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if quote.Receipt.WindowID != "" {
		t.Fatalf("unpinned quote picked window %q", quote.Receipt.WindowID)
	}
	if quote.Cost != 300_000_000 {
		t.Fatalf("cost = %d, want the unscaled base", quote.Cost)
	}
}

func TestNextBoundaryAfterFindsWindowChange(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "peak", StartMinuteUTC: 12 * 60, EndMinuteUTC: 14 * 60,
		WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1,
	})
	next := rules.NextBoundaryAfter(at(t, "2026-07-31T11:00:00Z"))
	if !next.Equal(at(t, "2026-07-31T12:00:00Z")) {
		t.Fatalf("next boundary = %s, want 12:00", next)
	}
	next = rules.NextBoundaryAfter(at(t, "2026-07-31T13:00:00Z"))
	if !next.Equal(at(t, "2026-07-31T14:00:00Z")) {
		t.Fatalf("next boundary = %s, want 14:00", next)
	}
}

func TestNextBoundaryWithNoWindowsIsZero(t *testing.T) {
	rules := peakRuleSet(t)
	if got := rules.NextBoundaryAfter(at(t, "2026-07-31T11:00:00Z")); !got.IsZero() {
		t.Fatalf("expected zero time, got %s", got)
	}
}

// Clock pinning is the whole point of passing an instant: a request quoted
// just before a boundary must settle at the rate it was quoted.
func TestPinnedInstantSurvivesBoundaryCrossing(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "peak", StartMinuteUTC: 16 * 60, EndMinuteUTC: 20 * 60,
		WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1,
	})
	pinned := at(t, "2026-07-31T15:59:58Z")
	usage := InputUsage(1_000_000)

	quoted, err := rules.QuoteAt(usage, pinned)
	if err != nil {
		t.Fatal(err)
	}
	// Settlement happens after the boundary but re-uses the pinned instant.
	settled, err := rules.QuoteAt(usage, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if quoted.Cost != settled.Cost {
		t.Fatalf("quote %d != settlement %d", quoted.Cost, settled.Cost)
	}
	if quoted.Receipt.WindowID != "" {
		t.Fatalf("pinned off-peak quote picked window %q", quoted.Receipt.WindowID)
	}
	// Sanity: the same usage priced inside the window really is more expensive.
	inside, err := rules.QuoteAt(usage, at(t, "2026-07-31T17:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if inside.Cost <= quoted.Cost {
		t.Fatal("the window is not actually more expensive; test is not proving anything")
	}
}

func TestMaxOutputTokensRespectsWindow(t *testing.T) {
	rules := peakRuleSet(t, Window{
		ID: "peak", StartMinuteUTC: 0, EndMinuteUTC: MinutesPerDay,
		WeekdayMask: allWeekdays, MultiplierNum: 2, MultiplierDen: 1,
	})
	budget := int64(1_000_000_000)
	peak := at(t, "2026-07-31T12:00:00Z")

	allowed, err := rules.MaxOutputTokensAt(InputUsage(1000), 10_000_000, moneyOf(budget), peak)
	if err != nil {
		t.Fatal(err)
	}
	usage := InputUsage(1000)
	usage.OutputTokens = allowed
	quote, err := rules.QuoteAt(usage, peak)
	if err != nil {
		t.Fatal(err)
	}
	if int64(quote.Cost) > budget {
		t.Fatalf("allowance %d costs %d, over budget %d", allowed, quote.Cost, budget)
	}
	// And one more token must break the budget, proving maximality under the
	// window (this is what fails if monotonicity is ever broken).
	usage.OutputTokens = allowed + 1
	next, err := rules.QuoteAt(usage, peak)
	if err != nil {
		t.Fatal(err)
	}
	if int64(next.Cost) <= budget {
		t.Fatalf("allowance %d was not maximal", allowed)
	}
}

func TestWindowValidationRejectsAmbiguousAndInvalid(t *testing.T) {
	cases := map[string]Window{
		"missing id":        {StartMinuteUTC: 0, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1},
		"equal boundaries":  {ID: "w", StartMinuteUTC: 60, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1},
		"zero denominator":  {ID: "w", StartMinuteUTC: 0, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 0},
		"empty weekdays":    {ID: "w", StartMinuteUTC: 0, EndMinuteUTC: 60, WeekdayMask: 0, MultiplierNum: 1, MultiplierDen: 1},
		"start out of range": {ID: "w", StartMinuteUTC: MinutesPerDay, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1},
		"unknown class":     {ID: "w", StartMinuteUTC: 0, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1, AppliesTo: []TokenClass{"nonsense"}},
	}
	for name, window := range cases {
		if err := window.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

func TestDuplicateWindowIDsRejected(t *testing.T) {
	_, err := New(Spec{
		ID:   "dupes",
		Base: Rates{InputPerMillion: 1},
		Windows: []Window{
			{ID: "same", StartMinuteUTC: 0, EndMinuteUTC: 60, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1},
			{ID: "same", StartMinuteUTC: 60, EndMinuteUTC: 120, WeekdayMask: 1, MultiplierNum: 1, MultiplierDen: 1},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate window ids to be rejected")
	}
}

func moneyOf(value int64) money.NanoUSD { return money.NanoUSD(value) }
