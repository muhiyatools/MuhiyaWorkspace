package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"gateway/money"
	"github.com/google/uuid"
)

var ErrBudgetExceeded = errors.New("insufficient remaining budget")

type BudgetExceededError struct {
	Requested money.NanoUSD
	Available money.NanoUSD
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("%s: requested $%s, available $%s", ErrBudgetExceeded, e.Requested, e.Available)
}

func (e *BudgetExceededError) Unwrap() error { return ErrBudgetExceeded }

type budgetWindowLimit struct {
	durationSeconds int
	budget          money.NanoUSD
}

// AvailableBudgetNano returns the amount one generation may consume from the
// user's ordinary budget windows and top-ups. There are no monetary holds:
// settled request logs are the only plan-window usage source.
func (db *DB) AvailableBudgetNano(ctx context.Context, userID string) (money.NanoUSD, error) {
	tx, err := db.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	available, err := availableBudgetNanoTx(ctx, tx, userID, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return available, tx.Commit()
}

func availableBudgetNanoTx(ctx context.Context, tx *sql.Tx, userID string, now time.Time) (money.NanoUSD, error) {
	var planID string
	var assigned time.Time
	var reset sql.NullTime
	if err := tx.QueryRowContext(ctx,
		"SELECT plan_id, plan_assigned_at, usage_reset_at FROM users WHERE id = $1 AND status = 'active'",
		userID).Scan(&planID, &assigned, &reset); err != nil {
		return 0, err
	}

	var topups money.NanoUSD
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_nano_usd - used_nano_usd), 0)
		FROM user_topups WHERE user_id = $1`+activeTopupFilter, userID).Scan(&topups); err != nil {
		return 0, err
	}
	windows, err := budgetWindowLimitsTx(ctx, tx, planID)
	if err != nil {
		return 0, err
	}
	if len(windows) == 0 {
		return money.NanoUSD(math.MaxInt64), nil
	}

	available := money.NanoUSD(math.MaxInt64)
	for _, window := range windows {
		floor := effectiveFloor(windowPeriodStart(assigned, window.durationSeconds, now), reset)
		spent, err := successfulSpendSinceTx(ctx, tx, userID, floor)
		if err != nil {
			return 0, err
		}
		candidate := window.budget - spent + topups
		if candidate < 0 {
			candidate = 0
		}
		if candidate < available {
			available = candidate
		}
	}
	return available, nil
}

func budgetWindowLimitsTx(ctx context.Context, tx *sql.Tx, planID string) ([]budgetWindowLimit, error) {
	rows, err := tx.QueryContext(ctx, `SELECT duration_seconds, budget_nano_usd
		FROM budget_windows WHERE plan_id = $1 AND budget_nano_usd > 0`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windows []budgetWindowLimit
	for rows.Next() {
		var window budgetWindowLimit
		if err := rows.Scan(&window.durationSeconds, &window.budget); err != nil {
			return nil, err
		}
		windows = append(windows, window)
	}
	return windows, rows.Err()
}

func successfulSpendSinceTx(ctx context.Context, tx *sql.Tx, userID string, floor time.Time) (money.NanoUSD, error) {
	var spent money.NanoUSD
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_nano_usd), 0)
		FROM request_logs WHERE user_id = $1 AND created_at >= $2
		AND status_code >= 200 AND status_code < 300`, userID, floor).Scan(&spent)
	return spent, err
}

// SettleUsageAndLog atomically records actual usage, charges any plan overage
// to top-ups, and appends the account ledger debit. The caller-supplied ceiling
// is the pre-upstream affordable quote; a provider can never charge the user
// above it even if it ignores max_tokens.
func (db *DB) SettleUsageAndLog(ctx context.Context, entry RequestLog) error {
	if err := normalizeSettledEntry(&entry); err != nil {
		return err
	}
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", entry.UserID); err != nil {
		return err
	}
	exists, err := requestLogExistsTx(ctx, tx, entry.ID)
	if err != nil || exists {
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if entry.CostNanoUSD > 0 {
		topupCharge, err := marginalTopupChargeTx(ctx, tx, entry)
		if err != nil {
			return err
		}
		if err := deductNanoFromTopupsTx(ctx, tx, entry.UserID, topupCharge); err != nil {
			return err
		}
	}
	if err := insertRequestLog(tx, entry); err != nil {
		return err
	}
	if err := appendUsageDebitTx(ctx, tx, entry); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeSettledEntry(entry *RequestLog) error {
	if err := normalizeRequestLogMoney(entry); err != nil {
		return err
	}
	if entry.StatusCode < 200 || entry.StatusCode >= 300 {
		entry.Cost = 0
		entry.CostNanoUSD = 0
	}
	if entry.ChargeCeilingNanoUSD > 0 && entry.CostNanoUSD > entry.ChargeCeilingNanoUSD {
		entry.CostNanoUSD = entry.ChargeCeilingNanoUSD
		entry.Cost = entry.CostNanoUSD.USD()
		entry.UsageEstimated = true
		if entry.ErrorMessage != "" {
			entry.ErrorMessage += "; "
		}
		entry.ErrorMessage += "provider usage exceeded the affordable request ceiling; customer charge capped"
	}
	return nil
}

func requestLogExistsTx(ctx context.Context, tx *sql.Tx, requestID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM request_logs WHERE id = $1)", requestID).Scan(&exists)
	return exists, err
}

func appendUsageDebitTx(ctx context.Context, tx *sql.Tx, entry RequestLog) error {
	if entry.CostNanoUSD <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_ledger
		(id, user_id, request_id, kind, amount_nano_usd, idempotency_key, metadata)
		VALUES ($1,$2,$3,'debit',$4,$5,jsonb_build_object('charge_ceiling_nano_usd',$6))`,
		uuid.NewString(), entry.UserID, entry.ID, entry.CostNanoUSD,
		"settlement:"+entry.ID, entry.ChargeCeilingNanoUSD)
	return err
}

func marginalTopupChargeTx(ctx context.Context, tx *sql.Tx, entry RequestLog) (money.NanoUSD, error) {
	var planID string
	var assigned time.Time
	var reset sql.NullTime
	if err := tx.QueryRowContext(ctx, "SELECT plan_id, plan_assigned_at, usage_reset_at FROM users WHERE id = $1", entry.UserID).
		Scan(&planID, &assigned, &reset); err != nil {
		return 0, err
	}
	windows, err := budgetWindowLimitsTx(ctx, tx, planID)
	if err != nil {
		return 0, err
	}
	var maximum money.NanoUSD
	for _, window := range windows {
		floor := effectiveFloor(windowPeriodStart(assigned, window.durationSeconds, entry.CreatedAt), reset)
		spent, err := successfulSpendSinceTx(ctx, tx, entry.UserID, floor)
		if err != nil {
			return 0, err
		}
		before := money.Max(0, spent-window.budget)
		after := money.Max(0, spent+entry.CostNanoUSD-window.budget)
		if delta := after - before; delta > maximum {
			maximum = delta
		}
	}
	return maximum, nil
}

func deductNanoFromTopupsTx(ctx context.Context, tx *sql.Tx, userID string, amount money.NanoUSD) error {
	if amount <= 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, amount_nano_usd, used_nano_usd
		FROM user_topups WHERE user_id = $1 AND used_nano_usd < amount_nano_usd`+
		activeTopupFilter+` ORDER BY expires_at ASC NULLS LAST, created_at ASC FOR UPDATE`, userID)
	if err != nil {
		return err
	}
	type topup struct {
		id           string
		amount, used money.NanoUSD
	}
	var topups []topup
	for rows.Next() {
		var current topup
		if err := rows.Scan(&current.id, &current.amount, &current.used); err != nil {
			rows.Close()
			return err
		}
		topups = append(topups, current)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	remaining := amount
	for _, current := range topups {
		consume := money.Min(remaining, current.amount-current.used)
		if _, err := tx.ExecContext(ctx, `UPDATE user_topups
			SET used_nano_usd = used_nano_usd + $1,
			    used_credits = (used_nano_usd + $1)::double precision / 10000000.0
			WHERE id = $2`, consume, current.id); err != nil {
			return err
		}
		remaining -= consume
		if remaining == 0 {
			return nil
		}
	}
	return &BudgetExceededError{Requested: amount, Available: amount - remaining}
}
