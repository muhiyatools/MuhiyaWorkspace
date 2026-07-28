package db

type AdminOperations struct {
	Reservations []BudgetReservation  `json:"reservations"`
	Ledger       []AccountLedgerEntry `json:"ledger"`
	UsageResets  []UsageReset         `json:"usage_resets"`
}

func (db *DB) ListAdminOperations(limit int) (AdminOperations, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	reservations, err := db.listBudgetReservations(limit)
	if err != nil {
		return AdminOperations{}, err
	}
	ledger, err := db.listAccountLedger(limit)
	if err != nil {
		return AdminOperations{}, err
	}
	resets, err := db.listUsageResets(limit)
	if err != nil {
		return AdminOperations{}, err
	}
	return AdminOperations{Reservations: reservations, Ledger: ledger, UsageResets: resets}, nil
}

func (db *DB) listBudgetReservations(limit int) ([]BudgetReservation, error) {
	rows, err := db.conn.Query(`SELECT id, request_id, user_id,
		COALESCE(virtual_key_id,''), COALESCE(model_id,''), amount_nano_usd,
		settled_nano_usd, status, price_snapshot_id, lease_expires_at, created_at, updated_at
		FROM budget_reservations ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reservations []BudgetReservation
	for rows.Next() {
		var reservation BudgetReservation
		if err := rows.Scan(
			&reservation.ID, &reservation.RequestID, &reservation.UserID,
			&reservation.VirtualKeyID, &reservation.ModelID, &reservation.Amount,
			&reservation.Settled, &reservation.Status, &reservation.PriceSnapshot,
			&reservation.LeaseExpiresAt, &reservation.CreatedAt, &reservation.UpdatedAt,
		); err != nil {
			return nil, err
		}
		reservations = append(reservations, reservation)
	}
	return reservations, rows.Err()
}

func (db *DB) listAccountLedger(limit int) ([]AccountLedgerEntry, error) {
	rows, err := db.conn.Query(`SELECT id, user_id, COALESCE(request_id,''),
		COALESCE(reservation_id,''), kind, amount_nano_usd, idempotency_key,
		metadata::text, created_at FROM account_ledger ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AccountLedgerEntry
	for rows.Next() {
		var entry AccountLedgerEntry
		if err := rows.Scan(
			&entry.ID, &entry.UserID, &entry.RequestID, &entry.ReservationID,
			&entry.Kind, &entry.AmountNanoUSD, &entry.IdempotencyKey,
			&entry.Metadata, &entry.CreatedAt,
		); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (db *DB) listUsageResets(limit int) ([]UsageReset, error) {
	rows, err := db.conn.Query(`SELECT id, scope, COALESCE(user_id,''), note, created_at
		FROM usage_resets ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var resets []UsageReset
	for rows.Next() {
		var reset UsageReset
		if err := rows.Scan(&reset.ID, &reset.Scope, &reset.UserID, &reset.Note, &reset.CreatedAt); err != nil {
			return nil, err
		}
		resets = append(resets, reset)
	}
	return resets, rows.Err()
}
