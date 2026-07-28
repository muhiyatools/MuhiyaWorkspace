package db

func (db *DB) ListUsageResets(limit int) ([]UsageReset, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
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
