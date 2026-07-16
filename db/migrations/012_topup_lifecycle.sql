-- 012: top-up expiry + soft delete.
--
-- expires_at lets an admin grant credits that lapse on a date; deleted_at is a
-- soft delete that preserves the audit trail. Once a top-up is expired or deleted
-- it disappears from EVERY user-facing balance and from consumption (the four
-- money queries add the same active-topup filter), so the user never sees it —
-- not even as an expired or completed row. Admins keep seeing it, badged.
ALTER TABLE user_topups ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NULL;
ALTER TABLE user_topups ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL;

-- Consumption and balance sums scan by user; a partial index keeps the
-- active-topup lookups cheap as the table grows.
CREATE INDEX IF NOT EXISTS idx_user_topups_active
    ON user_topups(user_id)
    WHERE deleted_at IS NULL;
