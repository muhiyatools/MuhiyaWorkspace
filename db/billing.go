package db

import (
	"database/sql"
	"time"

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
	updateResult, err := tx.Exec("UPDATE users SET usage_reset_at = now() WHERE id = $1", userID)
	if err != nil {
		return err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(
		"INSERT INTO usage_resets (id, scope, user_id, note) VALUES ($1, 'user', $2, $3)",
		uuid.NewString(), userID, note,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// activeTopupFilter is the single definition of user-spendable top-ups.
const activeTopupFilter = " AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())"
