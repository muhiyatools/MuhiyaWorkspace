package db

import (
	"database/sql"
	"testing"
	"time"
)

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
