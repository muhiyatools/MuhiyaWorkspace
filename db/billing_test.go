package db

import (
	"database/sql"
	"testing"
	"time"

	"gateway/money"
)

// TestOverageChargeNano verifies the charge math that makes the credit
// deduction idempotent and concurrency-safe (finding F1): under budget, first
// crossing, replay, the concurrent double-read, a rolling-window drop (no refund),
// and incremental accumulation. This runs with no database — it is the verifiable
// core of the money path.
func TestOverageChargeNano(t *testing.T) {
	const budget = money.NanoUSD(9_500_000_000)

	// Under budget: nothing charged; watermark advances to current spend.
	if c, wm := overageChargeNano(9_000_000_000, budget, 8_500_000_000); c != 0 || wm != 9_000_000_000 {
		t.Fatalf("under budget: credits=%v watermark=%v", c, wm)
	}

	// First crossing from a watermark at budget: charge only the part above budget
	// (10.00-9.50 = $0.50 -> 50 credits).
	if c, wm := overageChargeNano(10_000_000_000, budget, 9_500_000_000); c != 500_000_000 || wm != 10_000_000_000 {
		t.Fatalf("first crossing: credits=%v watermark=%v", c, wm)
	}

	// Idempotent replay: same spend as the watermark charges nothing.
	if c, _ := overageChargeNano(10_000_000_000, budget, 10_000_000_000); c != 0 {
		t.Fatalf("replay must charge nothing, got %v", c)
	}

	// The concurrent double-read, expressed sequentially: two requests both observe
	// the combined spend 10.00. The first advances the watermark to 10.00; the
	// second (same spend) charges nothing. Total across both = ONE $0.50 overage —
	// this is exactly what the old spend-only formula double-charged.
	c1, wm1 := overageChargeNano(10_000_000_000, budget, 9_500_000_000)
	c2, _ := overageChargeNano(10_000_000_000, budget, wm1)
	if c1+c2 != 500_000_000 {
		t.Fatalf("concurrent double-read charged %v total, want $0.50", c1+c2)
	}

	// Rolling window rolled over / bonus reset raised the floor: spend fell below
	// the watermark. No refund (credits clamp at 0) and the watermark resets down.
	if c, wm := overageChargeNano(2_000_000_000, budget, 10_000_000_000); c != 0 || wm != 2_000_000_000 {
		t.Fatalf("rolling drop: credits=%v watermark=%v", c, wm)
	}

	// After a drop, climbing back over budget charges fresh from the low watermark.
	if c, _ := overageChargeNano(10_000_000_000, budget, 2_000_000_000); c != 500_000_000 {
		t.Fatalf("post-drop climb charged %v, want $0.50", c)
	}

	// Both watermark and current over budget: charge only the increment (0.25 USD).
	if c, _ := overageChargeNano(10_000_000_000, budget, 9_750_000_000); c != 250_000_000 {
		t.Fatalf("increment above budget charged %v, want $0.25", c)
	}
}

// TestEffectiveFloor pins the bonus-reset floor (Part B): a reset AFTER the period
// start zeroes current usage; a reset at/before it (or none) leaves the window's
// own boundary in charge. The reset never participates in reset_time (INV-5) — this
// only lowers the spend-sum bound.
func TestEffectiveFloor(t *testing.T) {
	period := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// No reset: floor is the period start.
	if got := effectiveFloor(period, sql.NullTime{}); !got.Equal(period) {
		t.Fatalf("no reset: floor = %v, want period start", got)
	}
	// Reset after the period start: floor jumps to the reset instant (usage zeroed).
	reset := period.Add(2 * time.Hour)
	if got := effectiveFloor(period, sql.NullTime{Time: reset, Valid: true}); !got.Equal(reset) {
		t.Fatalf("reset-after: floor = %v, want reset instant", got)
	}
	// Reset before the period start: the window already rolled past it — floor stays
	// at the period start (a stale reset never resurrects old spend or hides new).
	if got := effectiveFloor(period, sql.NullTime{Time: period.Add(-time.Hour), Valid: true}); !got.Equal(period) {
		t.Fatalf("reset-before: floor = %v, want period start", got)
	}
	// Reset exactly at the period start is a no-op (strict After).
	if got := effectiveFloor(period, sql.NullTime{Time: period, Valid: true}); !got.Equal(period) {
		t.Fatalf("reset-at: floor = %v, want period start", got)
	}
}

// TestWindowPeriodStart pins the rolling period boundary shared by the
// enforcement, display, and deduction paths.
func TestWindowPeriodStart(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 5h window (18000s), 12h after the anchor -> 2 full periods -> +10h.
	if got := windowPeriodStart(anchor, 18000, anchor.Add(12*time.Hour)); !got.Equal(anchor.Add(10 * time.Hour)) {
		t.Fatalf("5h window period start = %v, want %v", got, anchor.Add(10*time.Hour))
	}
	// Exactly on a boundary stays on it.
	if got := windowPeriodStart(anchor, 18000, anchor.Add(10*time.Hour)); !got.Equal(anchor.Add(10 * time.Hour)) {
		t.Fatalf("on-boundary period start = %v", got)
	}
	// A now before the anchor clamps to the anchor (never negative periods).
	if got := windowPeriodStart(anchor, 18000, anchor.Add(-time.Hour)); !got.Equal(anchor) {
		t.Fatalf("pre-anchor period start = %v, want anchor", got)
	}
	// Zero/negative duration returns the anchor (never divides by zero).
	if got := windowPeriodStart(anchor, 0, anchor.Add(12*time.Hour)); !got.Equal(anchor) {
		t.Fatalf("zero-duration period start = %v, want anchor", got)
	}
}
