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

var (
	ErrBudgetExceeded      = errors.New("insufficient remaining budget")
	ErrReservationNotFound = errors.New("budget reservation not found")
	ErrReservationClosed   = errors.New("budget reservation is no longer active")
	ErrReservationExceeded = errors.New("settled cost exceeds reserved amount")
)

type BudgetExceededError struct {
	Requested money.NanoUSD
	Available money.NanoUSD
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("%s: requested $%s, available $%s", ErrBudgetExceeded, e.Requested, e.Available)
}

func (e *BudgetExceededError) Unwrap() error { return ErrBudgetExceeded }

type ReserveBudgetRequest struct {
	RequestID     string
	UserID        string
	VirtualKeyID  string
	ModelID       string
	Amount        money.NanoUSD
	PriceSnapshot string
	LeaseDuration time.Duration
}

type BudgetReservation struct {
	ID             string        `json:"id"`
	RequestID      string        `json:"request_id"`
	UserID         string        `json:"user_id"`
	VirtualKeyID   string        `json:"virtual_key_id"`
	ModelID        string        `json:"model_id"`
	Amount         money.NanoUSD `json:"amount_nano_usd"`
	Settled        money.NanoUSD `json:"settled_nano_usd"`
	Status         string        `json:"status"`
	PriceSnapshot  string        `json:"price_snapshot_id"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type budgetWindowLimit struct {
	durationSeconds int
	budget          money.NanoUSD
}

// ReserveBudget is the authoritative pre-upstream admission gate. It serializes
// requests per user and counts both committed spend and live reservations, so
// concurrent generations cannot each consume the same remaining dollars.
func (db *DB) ReserveBudget(ctx context.Context, request ReserveBudgetRequest) (*BudgetReservation, error) {
	if request.RequestID == "" || request.UserID == "" || request.PriceSnapshot == "" {
		return nil, fmt.Errorf("request id, user id, and price snapshot are required")
	}
	if request.Amount < 0 {
		return nil, money.ErrNegative
	}
	if request.LeaseDuration <= 0 {
		request.LeaseDuration = 35 * time.Minute
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", request.UserID); err != nil {
		return nil, err
	}

	existing, err := reservationByRequestIDTx(ctx, tx, request.RequestID)
	if err != nil && !errors.Is(err, ErrReservationNotFound) {
		return nil, err
	}
	if existing != nil {
		if existing.UserID != request.UserID || existing.ModelID != request.ModelID ||
			existing.Amount != request.Amount || existing.PriceSnapshot != request.PriceSnapshot {
			return nil, fmt.Errorf("request id %q already has a different reservation", request.RequestID)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE budget_reservations
		SET status = 'expired', updated_at = $1
		WHERE user_id = $2 AND status = 'reserved' AND lease_expires_at <= $1`, now, request.UserID); err != nil {
		return nil, err
	}

	available, err := availableBudgetNanoTx(ctx, tx, request.UserID, now)
	if err != nil {
		return nil, err
	}
	if available != money.NanoUSD(math.MaxInt64) && request.Amount > available {
		return nil, &BudgetExceededError{Requested: request.Amount, Available: available}
	}

	reservation := &BudgetReservation{
		ID:             uuid.NewString(),
		RequestID:      request.RequestID,
		UserID:         request.UserID,
		VirtualKeyID:   request.VirtualKeyID,
		ModelID:        request.ModelID,
		Amount:         request.Amount,
		Status:         "reserved",
		PriceSnapshot:  request.PriceSnapshot,
		LeaseExpiresAt: now.Add(request.LeaseDuration),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	var keyID, modelID any
	if reservation.VirtualKeyID != "" {
		keyID = reservation.VirtualKeyID
	}
	if reservation.ModelID != "" {
		modelID = reservation.ModelID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO budget_reservations
		(id, request_id, user_id, virtual_key_id, model_id, amount_nano_usd,
		 settled_nano_usd, status, price_snapshot_id, lease_expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,0,'reserved',$7,$8,$9,$9)`,
		reservation.ID, reservation.RequestID, reservation.UserID, keyID, modelID,
		reservation.Amount, reservation.PriceSnapshot, reservation.LeaseExpiresAt, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return reservation, nil
}

// AvailableBudgetNano returns what one new request may reserve right now. A
// MaxInt64 result means the plan has no enabled monetary windows.
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

	var reserved int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_nano_usd), 0)
		FROM budget_reservations WHERE user_id = $1 AND status = 'reserved' AND lease_expires_at > $2`,
		userID, now).Scan(&reserved); err != nil {
		return 0, err
	}
	var topups int64
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
		candidate := window.budget - spent - money.NanoUSD(reserved) + money.NanoUSD(topups)
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

// SettleReservationAndLog atomically writes usage, consumes top-ups when the
// request crossed a free window, appends the immutable ledger debit, and closes
// the reservation. Replays are idempotent by reservation and request IDs.
func (db *DB) SettleReservationAndLog(ctx context.Context, entry RequestLog) error {
	if entry.ReservationID == "" {
		return fmt.Errorf("reservation id is required")
	}
	if err := normalizeRequestLogMoney(&entry); err != nil {
		return err
	}
	if entry.StatusCode < 200 || entry.StatusCode >= 300 {
		entry.Cost = 0
		entry.CostNanoUSD = 0
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var reservation BudgetReservation
	if err := tx.QueryRowContext(ctx, `SELECT id, request_id, user_id,
		COALESCE(virtual_key_id,''), COALESCE(model_id,''), amount_nano_usd,
		settled_nano_usd, status, price_snapshot_id, lease_expires_at, created_at, updated_at
		FROM budget_reservations WHERE id = $1 FOR UPDATE`, entry.ReservationID).
		Scan(&reservation.ID, &reservation.RequestID, &reservation.UserID,
			&reservation.VirtualKeyID, &reservation.ModelID, &reservation.Amount,
			&reservation.Settled, &reservation.Status, &reservation.PriceSnapshot,
			&reservation.LeaseExpiresAt, &reservation.CreatedAt, &reservation.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReservationNotFound
		}
		return err
	}
	if reservation.RequestID != entry.ID || reservation.UserID != entry.UserID {
		return fmt.Errorf("request log does not match reservation")
	}
	if reservation.Status == "settled" {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM request_logs WHERE reservation_id = $1)", reservation.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return tx.Commit()
		}
		return fmt.Errorf("settled reservation has no request log")
	}
	if reservation.Status != "reserved" {
		return ErrReservationClosed
	}
	if entry.CostNanoUSD > reservation.Amount {
		// The upstream violated its declared ceiling or reported more prompt
		// usage than the conservative admission bound. Never debit beyond what
		// admission authorized; preserve the usage counters and mark the row so
		// reconciliation/operations can recover the provider-side difference.
		entry.CostNanoUSD = reservation.Amount
		entry.Cost = reservation.Amount.USD()
		entry.UsageEstimated = true
		if entry.ErrorMessage != "" {
			entry.ErrorMessage += "; "
		}
		entry.ErrorMessage += ErrReservationExceeded.Error() + "; customer charge capped at reservation"
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", entry.UserID); err != nil {
		return err
	}

	if entry.CostNanoUSD > 0 {
		topUpCharge, err := marginalTopupChargeTx(ctx, tx, entry.UserID, entry.CostNanoUSD, entry.CreatedAt)
		if err != nil {
			return err
		}
		if err := deductNanoFromTopupsTx(ctx, tx, entry.UserID, topUpCharge); err != nil {
			return err
		}
	}
	if err := insertRequestLog(tx, entry); err != nil {
		return err
	}
	if entry.CostNanoUSD > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_ledger
			(id, user_id, request_id, reservation_id, kind, amount_nano_usd, idempotency_key, metadata)
			VALUES ($1,$2,$3,$4,'debit',$5,$6,jsonb_build_object('price_snapshot_id',$7))`,
			uuid.NewString(), entry.UserID, entry.ID, reservation.ID, entry.CostNanoUSD,
			"settlement:"+reservation.ID, reservation.PriceSnapshot); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE budget_reservations
		SET status = 'settled', settled_nano_usd = $2, updated_at = now()
		WHERE id = $1`, reservation.ID, entry.CostNanoUSD); err != nil {
		return err
	}
	return tx.Commit()
}

func marginalTopupChargeTx(ctx context.Context, tx *sql.Tx, userID string, cost money.NanoUSD, at time.Time) (money.NanoUSD, error) {
	var planID string
	var assigned time.Time
	var reset sql.NullTime
	if err := tx.QueryRowContext(ctx, "SELECT plan_id, plan_assigned_at, usage_reset_at FROM users WHERE id = $1", userID).
		Scan(&planID, &assigned, &reset); err != nil {
		return 0, err
	}
	windows, err := budgetWindowLimitsTx(ctx, tx, planID)
	if err != nil {
		return 0, err
	}
	var maximum money.NanoUSD
	for _, window := range windows {
		floor := effectiveFloor(windowPeriodStart(assigned, window.durationSeconds, at), reset)
		spent, err := successfulSpendSinceTx(ctx, tx, userID, floor)
		if err != nil {
			return 0, err
		}
		before := money.Max(0, spent-window.budget)
		after := money.Max(0, spent+cost-window.budget)
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
		var item topup
		if err := rows.Scan(&item.id, &item.amount, &item.used); err != nil {
			rows.Close()
			return err
		}
		topups = append(topups, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	remaining := amount
	for _, item := range topups {
		consume := money.Min(remaining, item.amount-item.used)
		if _, err := tx.ExecContext(ctx, `UPDATE user_topups
			SET used_nano_usd = used_nano_usd + $1,
			    used_credits = (used_nano_usd + $1)::double precision / 10000000.0
			WHERE id = $2`, consume, item.id); err != nil {
			return err
		}
		remaining -= consume
		if remaining == 0 {
			return nil
		}
	}
	if remaining > 0 {
		return &BudgetExceededError{Requested: amount, Available: amount - remaining}
	}
	return nil
}

func (db *DB) ReleaseReservation(ctx context.Context, reservationID string) error {
	result, err := db.conn.ExecContext(ctx, `UPDATE budget_reservations
		SET status = 'released', updated_at = now()
		WHERE id = $1 AND status = 'reserved'`, reservationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		var status string
		if err := db.conn.QueryRowContext(ctx, "SELECT status FROM budget_reservations WHERE id = $1", reservationID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrReservationNotFound
			}
			return err
		}
		if status != "released" && status != "settled" {
			return ErrReservationClosed
		}
	}
	return nil
}

func (db *DB) ExpireReservations(ctx context.Context) (int64, error) {
	return db.ReconcileExpiredReservations(ctx, 100)
}

// ReconcileExpiredReservations conservatively settles abandoned reservations
// at their authorized ceiling. Callers release reservations explicitly when an
// upstream was never contacted or returned a known zero-cost failure; an
// expired live reservation therefore represents indeterminate provider usage.
func (db *DB) ReconcileExpiredReservations(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT id, request_id, user_id,
		COALESCE(virtual_key_id,''), COALESCE(model_id,''), amount_nano_usd, created_at
		FROM budget_reservations
		WHERE status = 'reserved' AND lease_expires_at <= now()
		ORDER BY lease_expires_at ASC LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type abandoned struct {
		reservationID, requestID, userID, keyID, modelID string
		amount                                           money.NanoUSD
		created                                          time.Time
	}
	var pending []abandoned
	for rows.Next() {
		var item abandoned
		if err := rows.Scan(&item.reservationID, &item.requestID, &item.userID,
			&item.keyID, &item.modelID, &item.amount, &item.created); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var reconciled int64
	for _, item := range pending {
		entry := RequestLog{
			ID:             item.requestID,
			ReservationID:  item.reservationID,
			VirtualKeyID:   item.keyID,
			UserID:         item.userID,
			ModelID:        item.modelID,
			RequestPath:    "reconciliation/expired-reservation",
			StatusCode:     299,
			CostNanoUSD:    item.amount,
			Cost:           item.amount.USD(),
			ErrorMessage:   "provider usage unavailable after process interruption; settled conservatively",
			ClientApp:      "MuhiyaGateway",
			RequestedModel: item.modelID,
			Complexity:     "reconciliation",
			UsageEstimated: true,
			CreatedAt:      item.created,
		}
		if err := db.SettleReservationAndLog(ctx, entry); err != nil {
			if errors.Is(err, ErrReservationClosed) {
				continue
			}
			return reconciled, err
		}
		reconciled++
	}
	return reconciled, nil
}

func reservationByRequestIDTx(ctx context.Context, tx *sql.Tx, requestID string) (*BudgetReservation, error) {
	var reservation BudgetReservation
	err := tx.QueryRowContext(ctx, `SELECT id, request_id, user_id,
		COALESCE(virtual_key_id,''), COALESCE(model_id,''), amount_nano_usd,
		settled_nano_usd, status, price_snapshot_id, lease_expires_at, created_at, updated_at
		FROM budget_reservations WHERE request_id = $1`, requestID).
		Scan(&reservation.ID, &reservation.RequestID, &reservation.UserID,
			&reservation.VirtualKeyID, &reservation.ModelID, &reservation.Amount,
			&reservation.Settled, &reservation.Status, &reservation.PriceSnapshot,
			&reservation.LeaseExpiresAt, &reservation.CreatedAt, &reservation.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReservationNotFound
	}
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}
