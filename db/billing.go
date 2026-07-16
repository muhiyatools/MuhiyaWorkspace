package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Credit accounting math and helpers behind DeductExtraCreditsIfExceeded. The
// arithmetic lives here as pure functions (overageChargeCredits, windowPeriodStart)
// so it is unit-tested without a database; the *sql.Tx helpers are the thin data
// layer the transactional deduction composes. See migration 010 for the watermark
// that makes the whole operation idempotent (finding F1).

// creditsPerUSD converts a USD overage into top-up credits: 1 credit = $0.01, so
// $1 of over-budget spend consumes 100 credits. Single source of truth for the
// conversion (previously a bare 100.0 literal in the deduction).
const creditsPerUSD = 100.0

// windowPeriodStart returns the start of the budget window's CURRENT rolling
// period: the most recent multiple of the window duration since the plan was
// assigned. A future or equal anchor yields the anchor itself. Pure and shared by
// the enforcement, display, and deduction paths so the period boundary can never
// be computed three subtly different ways.
func windowPeriodStart(planAssignedAt time.Time, durationSeconds int, now time.Time) time.Time {
	if durationSeconds <= 0 {
		return planAssignedAt
	}
	duration := time.Duration(durationSeconds) * time.Second
	elapsed := now.Sub(planAssignedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	periods := int64(elapsed / duration)
	return planAssignedAt.Add(time.Duration(periods) * duration)
}

// effectiveFloor is the lower bound of the spend sum for a window: the later of
// the rolling period start and any admin bonus-reset floor (migration 011). It is
// how a "gift" reset zeroes current usage without moving the scheduled reset — the
// caller keeps deriving reset_time from plan_assigned_at alone (INV-5).
func effectiveFloor(periodStart time.Time, usageResetAt sql.NullTime) time.Time {
	if usageResetAt.Valid && usageResetAt.Time.After(periodStart) {
		return usageResetAt.Time
	}
	return periodStart
}

// ResetAllUsersUsage clears every user's current in-window usage by raising the
// usage floor to now WITHOUT touching plan_assigned_at, so scheduled reset times
// are unchanged (the bonus "gift" reset, INV-5). It records one audit row and
// returns the number of users affected.
func (db *DB) ResetAllUsersUsage(note string) (int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE users SET usage_reset_at = now()")
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if _, err := tx.Exec("INSERT INTO usage_resets (id, scope, user_id, note) VALUES ($1, 'all', NULL, $2)", uuid.NewString(), note); err != nil {
		return 0, err
	}
	return affected, tx.Commit()
}

// ResetUserUsage clears one user's current in-window usage (same semantics as
// ResetAllUsersUsage, scoped to a single user).
func (db *DB) ResetUserUsage(userID, note string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE users SET usage_reset_at = now() WHERE id = $1", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO usage_resets (id, scope, user_id, note) VALUES ($1, 'user', $2, $3)", uuid.NewString(), userID, note); err != nil {
		return err
	}
	return tx.Commit()
}

// overBudget is the over-budget portion of a spend level (0 when at or under).
func overBudget(spendUSD, budgetUSD float64) float64 {
	if d := spendUSD - budgetUSD; d > 0 {
		return d
	}
	return 0
}

// overageChargeCredits computes how many top-up credits to charge for ONE budget
// window given its current in-period spend, its free budget, and the spend level
// already billed for the window (the watermark). It returns the credits to charge
// (>= 0) and the new watermark to persist.
//
// The charge is the INCREASE in over-budget spend above the watermark. This makes
// the deduction idempotent and concurrency-safe (finding F1): re-computing with
// the same currentSpend charges nothing, so two concurrent requests that both see
// the combined spend charge the overage exactly once between them. A window whose
// spend fell below the watermark (its rolling period rolled over, or a bonus reset
// raised the usage floor) resets the watermark down to the new spend WITHOUT
// refunding — the delta is clamped at zero.
//
// For serial traffic this reduces to the previous per-request marginal charge
// (when the watermark is seeded to currentSpend-cost on first sight), so it is
// behavior-preserving where it used to be correct and only removes the double
// charge where it was not.
func overageChargeCredits(currentSpendUSD, budgetUSD, lastBilledUSD float64) (credits, newLastBilled float64) {
	delta := overBudget(currentSpendUSD, budgetUSD) - overBudget(lastBilledUSD, budgetUSD)
	if delta < 0 {
		delta = 0
	}
	return delta * creditsPerUSD, currentSpendUSD
}

// listBudgetWindowsByPlanTx reads a plan's budget windows within a transaction,
// so the deduction sees a consistent snapshot under its advisory lock.
func listBudgetWindowsByPlanTx(tx *sql.Tx, planID string) ([]BudgetWindow, error) {
	rows, err := tx.Query("SELECT id, plan_id, name, duration_seconds, budget_usd, created_at FROM budget_windows WHERE plan_id = $1 ORDER BY duration_seconds ASC", planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []BudgetWindow{}
	for rows.Next() {
		var bw BudgetWindow
		if err := rows.Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, bw)
	}
	return list, rows.Err()
}

// spendInWindowTx sums a user's 2xx spend since periodStart within the tx (the
// same row set the enforcement and display paths sum).
func spendInWindowTx(tx *sql.Tx, userID string, periodStart time.Time) (float64, error) {
	var total float64
	err := tx.QueryRow(
		"SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300",
		userID, periodStart,
	).Scan(&total)
	return total, err
}

// loadChargeWatermark reads — creating on first sight — the per-window charge
// watermark under the transaction's row lock. On first sight it initializes to
// currentSpend-cost (clamped >= 0) so the first charge accounts only for THIS
// request's marginal over-budget contribution, matching the pre-watermark
// behavior. The advisory lock the caller holds makes the create-or-read race-free.
func loadChargeWatermark(tx *sql.Tx, userID, windowID string, currentSpendUSD, costUSD float64) (float64, error) {
	initial := currentSpendUSD - costUSD
	if initial < 0 {
		initial = 0
	}
	var lastBilled float64
	err := tx.QueryRow(
		`INSERT INTO user_window_charge_state (user_id, window_id, last_billed_spend)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, window_id) DO UPDATE SET user_id = EXCLUDED.user_id
		 RETURNING last_billed_spend`,
		userID, windowID, initial,
	).Scan(&lastBilled)
	return lastBilled, err
}

// saveChargeWatermark persists the window's new watermark within the tx.
func saveChargeWatermark(tx *sql.Tx, userID, windowID string, lastBilledSpend float64) error {
	_, err := tx.Exec(
		"UPDATE user_window_charge_state SET last_billed_spend = $3, updated_at = now() WHERE user_id = $1 AND window_id = $2",
		userID, windowID, lastBilledSpend,
	)
	return err
}

// activeTopupFilter scopes a user's top-ups to those a USER can still draw on:
// not soft-deleted and not past expiry (migration 012). It is appended to every
// user-facing balance/consumption query so an expired or deleted top-up vanishes
// from the user's credits everywhere at once (INV-6) — the single source of truth
// for "usable top-up" so the four money sites can never drift apart.
const activeTopupFilter = " AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())"

// deductFromTopups consumes `credits` from the user's usable top-ups within the
// tx, SOONEST-EXPIRING first so time-limited gifts burn before permanent credit
// and are not stranded, locking the rows FOR UPDATE. It stops when the demand is
// met or the top-ups are exhausted (an over-budget user with no credits is simply
// not charged here; enforcement blocks them elsewhere).
func deductFromTopups(tx *sql.Tx, userID string, credits float64) error {
	rows, err := tx.Query("SELECT id, credits, used_credits FROM user_topups WHERE user_id = $1 AND used_credits < credits"+activeTopupFilter+" ORDER BY expires_at ASC NULLS LAST, created_at ASC FOR UPDATE", userID)
	if err != nil {
		return err
	}
	type topupRow struct {
		id                   string
		credits, usedCredits float64
	}
	var active []topupRow
	for rows.Next() {
		var r topupRow
		if err := rows.Scan(&r.id, &r.credits, &r.usedCredits); err != nil {
			rows.Close()
			return err
		}
		active = append(active, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	remaining := credits
	for _, r := range active {
		if remaining <= 0 {
			break
		}
		avail := r.credits - r.usedCredits
		toAdd := remaining
		if toAdd > avail {
			toAdd = avail
		}
		if _, err := tx.Exec("UPDATE user_topups SET used_credits = used_credits + $1 WHERE id = $2", toAdd, r.id); err != nil {
			return err
		}
		remaining -= toAdd
	}
	return nil
}
