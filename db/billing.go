package db

import (
	"database/sql"
	"time"

	"gateway/money"
	"github.com/google/uuid"
)

func windowPeriodStart(planAssignedAt time.Time, durationSeconds int, now time.Time) time.Time {
	if durationSeconds <= 0 {
		return planAssignedAt
	}
	duration := time.Duration(durationSeconds) * time.Second
	elapsed := now.Sub(planAssignedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return planAssignedAt.Add(time.Duration(int64(elapsed/duration)) * duration)
}

func effectiveFloor(periodStart time.Time, usageResetAt sql.NullTime) time.Time {
	if usageResetAt.Valid && usageResetAt.Time.After(periodStart) {
		return usageResetAt.Time
	}
	return periodStart
}

func (db *DB) ResetAllUsersUsage(note string) (int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE users SET usage_reset_at = now()")
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	if _, err := tx.Exec(
		"INSERT INTO usage_resets (id, scope, user_id, note) VALUES ($1, 'all', NULL, $2)",
		uuid.NewString(), note,
	); err != nil {
		return 0, err
	}
	return affected, tx.Commit()
}

func (db *DB) ResetUserUsage(userID, note string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE users SET usage_reset_at = now() WHERE id = $1", userID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"INSERT INTO usage_resets (id, scope, user_id, note) VALUES ($1, 'user', $2, $3)",
		uuid.NewString(), userID, note,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func listBudgetWindowsByPlanTx(tx *sql.Tx, planID string) ([]BudgetWindow, error) {
	rows, err := tx.Query(`SELECT id, plan_id, name, duration_seconds,
		budget_usd, budget_nano_usd, created_at
		FROM budget_windows WHERE plan_id = $1 ORDER BY duration_seconds ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windows []BudgetWindow
	for rows.Next() {
		var window BudgetWindow
		if err := rows.Scan(
			&window.ID, &window.PlanID, &window.Name, &window.DurationSeconds,
			&window.BudgetUSD, &window.BudgetNanoUSD, &window.CreatedAt,
		); err != nil {
			return nil, err
		}
		windows = append(windows, window)
	}
	return windows, rows.Err()
}

func spendNanoInWindowTx(tx *sql.Tx, userID string, periodStart time.Time) (money.NanoUSD, error) {
	var total int64
	err := tx.QueryRow(`SELECT COALESCE(SUM(cost_nano_usd), 0)
		FROM request_logs WHERE user_id = $1 AND created_at >= $2
		AND status_code >= 200 AND status_code < 300`, userID, periodStart).Scan(&total)
	return money.NanoUSD(total), err
}

// overageChargeNano returns the increase in one window's exact over-budget
// spend and the watermark that makes replays/concurrent settlement idempotent.
func overageChargeNano(current, budget, lastBilled money.NanoUSD) (money.NanoUSD, money.NanoUSD) {
	currentOverage := money.Max(0, current-budget)
	previousOverage := money.Max(0, lastBilled-budget)
	if currentOverage <= previousOverage {
		return 0, current
	}
	return currentOverage - previousOverage, current
}

func loadChargeWatermarkNano(
	tx *sql.Tx,
	userID, windowID string,
	current, cost money.NanoUSD,
) (money.NanoUSD, error) {
	initial := current - cost
	if initial < 0 {
		initial = 0
	}
	var lastBilled int64
	err := tx.QueryRow(`INSERT INTO user_window_charge_state
		(user_id, window_id, last_billed_spend, last_billed_nano_usd)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, window_id) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING last_billed_nano_usd`,
		userID, windowID, initial.USD(), initial,
	).Scan(&lastBilled)
	return money.NanoUSD(lastBilled), err
}

func saveChargeWatermarkNano(
	tx *sql.Tx,
	userID, windowID string,
	lastBilled money.NanoUSD,
) error {
	_, err := tx.Exec(`UPDATE user_window_charge_state
		SET last_billed_spend = $3, last_billed_nano_usd = $4, updated_at = now()
		WHERE user_id = $1 AND window_id = $2`,
		userID, windowID, lastBilled.USD(), lastBilled,
	)
	return err
}

// activeTopupFilter is the single definition of user-spendable top-ups.
const activeTopupFilter = " AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())"
