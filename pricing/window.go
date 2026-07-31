package pricing

import (
	"fmt"
	"time"
)

// MinutesPerDay bounds every window boundary. All window arithmetic is in UTC
// so there is no daylight-saving class of bug: a provider's peak hours are
// published in UTC and evaluated in UTC.
const MinutesPerDay = 24 * 60

// Window scales the resolved rates during part of the day, which is how
// providers express peak/off-peak pricing ("2x during peak hours").
//
// The scale is an exact rational rather than a float or a percentage so that
// a doubled price is exactly doubled. Windows never stack: exactly one applies
// to a request, because compounding multipliers is unauditable and one
// mis-entered overlapping row could silently quadruple a customer's bill.
type Window struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	// StartMinuteUTC is inclusive, EndMinuteUTC exclusive. Start > End means
	// the window wraps midnight, e.g. 16:30 -> 00:30 spans 990..1470.
	StartMinuteUTC int `json:"start_minute_utc"`
	EndMinuteUTC   int `json:"end_minute_utc"`

	// WeekdayMask is a bitmask of ISO weekdays with Monday as bit 0.
	// It is evaluated against the weekday of the instant being priced, which
	// for a midnight-wrapping window means the post-midnight hours are tested
	// against the following day.
	WeekdayMask int `json:"weekday_mask"`

	MultiplierNum int64 `json:"multiplier_num"`
	MultiplierDen int64 `json:"multiplier_den"`

	// AppliesTo restricts the scale to specific token classes.
	// Empty means every class.
	AppliesTo []TokenClass `json:"applies_to,omitempty"`

	Priority       int        `json:"priority"`
	EffectiveFrom  *time.Time `json:"effective_from,omitempty"`
	EffectiveUntil *time.Time `json:"effective_until,omitempty"`
}

const allWeekdays = 127

func (w Window) Validate() error {
	if w.ID == "" {
		return fmt.Errorf("price window id is required")
	}
	if w.StartMinuteUTC < 0 || w.StartMinuteUTC >= MinutesPerDay {
		return fmt.Errorf("window %s: start minute %d out of range", w.ID, w.StartMinuteUTC)
	}
	if w.EndMinuteUTC < 0 || w.EndMinuteUTC > MinutesPerDay {
		return fmt.Errorf("window %s: end minute %d out of range", w.ID, w.EndMinuteUTC)
	}
	if w.StartMinuteUTC == w.EndMinuteUTC {
		// Zero-length or whole-day is ambiguous; make the operator say which.
		return fmt.Errorf("window %s: start and end minute are equal, which is ambiguous; "+
			"use 0..1440 for a whole-day window", w.ID)
	}
	if w.WeekdayMask < 1 || w.WeekdayMask > allWeekdays {
		return fmt.Errorf("window %s: weekday mask %d must select at least one day", w.ID, w.WeekdayMask)
	}
	if w.MultiplierDen <= 0 {
		return fmt.Errorf("window %s: multiplier denominator must be positive", w.ID)
	}
	if w.MultiplierNum < 0 {
		return fmt.Errorf("window %s: multiplier numerator cannot be negative", w.ID)
	}
	for _, class := range w.AppliesTo {
		if !validClass(class) {
			return fmt.Errorf("window %s: unknown token class %q", w.ID, class)
		}
	}
	if w.EffectiveFrom != nil && w.EffectiveUntil != nil &&
		!w.EffectiveUntil.After(*w.EffectiveFrom) {
		return fmt.Errorf("window %s: effective_until must be after effective_from", w.ID)
	}
	return nil
}

func validClass(class TokenClass) bool {
	for _, known := range TokenClasses {
		if known == class {
			return true
		}
	}
	return false
}

// Matches reports whether this window governs the given instant.
func (w Window) Matches(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	utc := at.UTC()
	if w.EffectiveFrom != nil && utc.Before(*w.EffectiveFrom) {
		return false
	}
	if w.EffectiveUntil != nil && !utc.Before(*w.EffectiveUntil) {
		return false
	}
	if w.WeekdayMask&weekdayBit(utc) == 0 {
		return false
	}
	return w.coversMinute(utc.Hour()*60 + utc.Minute())
}

func (w Window) coversMinute(minute int) bool {
	if w.StartMinuteUTC < w.EndMinuteUTC {
		return minute >= w.StartMinuteUTC && minute < w.EndMinuteUTC
	}
	// Wraps midnight: the evening segment or the early-morning segment.
	return minute >= w.StartMinuteUTC || minute < w.EndMinuteUTC
}

// weekdayBit maps a time onto the ISO weekday bit, Monday = bit 0.
func weekdayBit(at time.Time) int {
	iso := (int(at.Weekday()) + 6) % 7
	return 1 << iso
}

// scaleFor returns the exact multiplier this window applies to one class.
func (w Window) scaleFor(class TokenClass) (num, den int64) {
	if len(w.AppliesTo) == 0 {
		return w.MultiplierNum, w.MultiplierDen
	}
	for _, applies := range w.AppliesTo {
		if applies == class {
			return w.MultiplierNum, w.MultiplierDen
		}
	}
	return 1, 1
}

// IsIdentity reports whether the window leaves prices unchanged, which lets
// callers describe a 1x window honestly rather than implying a discount.
func (w Window) IsIdentity() bool {
	return w.MultiplierNum == w.MultiplierDen
}

// WindowFor returns the single window governing an instant: the highest
// priority match, ties broken by ID so selection is deterministic.
func (r RuleSet) WindowFor(at time.Time) *Window {
	var selected *Window
	for i := range r.Windows {
		candidate := &r.Windows[i]
		if !candidate.Matches(at) {
			continue
		}
		if selected == nil ||
			candidate.Priority > selected.Priority ||
			(candidate.Priority == selected.Priority && candidate.ID < selected.ID) {
			selected = candidate
		}
	}
	return selected
}

// NextBoundaryAfter returns the next instant at which the effective window
// selection could change, so callers can tell a user how long the current
// price lasts. It scans minute boundaries for up to eight days and returns the
// zero time when no change is found (no windows, or one permanent window).
func (r RuleSet) NextBoundaryAfter(at time.Time) time.Time {
	if len(r.Windows) == 0 || at.IsZero() {
		return time.Time{}
	}
	current := windowID(r.WindowFor(at))
	cursor := at.UTC().Truncate(time.Minute)
	limit := cursor.Add(8 * 24 * time.Hour)
	for cursor.Before(limit) {
		cursor = cursor.Add(time.Minute)
		if windowID(r.WindowFor(cursor)) != current {
			return cursor
		}
	}
	return time.Time{}
}

func windowID(w *Window) string {
	if w == nil {
		return ""
	}
	return w.ID
}
