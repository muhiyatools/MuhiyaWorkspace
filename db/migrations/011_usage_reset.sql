-- 011: admin bonus budget-reset (the "gift" reset).
--
-- Lets an admin instantly clear a user's (or every user's) CURRENT in-window
-- usage WITHOUT moving any scheduled reset time. Budget windows are derived
-- (spend summed since the period start, which is anchored to users.plan_assigned_at),
-- so raising a per-user usage floor to now() zeroes the current spend while
-- plan_assigned_at — and therefore every window's next reset instant — stays
-- exactly where it was. The three spend-sum paths take GREATEST(period_start,
-- usage_reset_at); the displayed reset_time keeps deriving from plan_assigned_at
-- only (invariant: scheduled resets are never shifted).
ALTER TABLE users ADD COLUMN IF NOT EXISTS usage_reset_at TIMESTAMPTZ NULL;

-- Audit trail for every bonus reset (who/when is out of scope here — the gateway
-- admin API is single-credential; the note carries the reason, e.g. "launch gift").
CREATE TABLE IF NOT EXISTS usage_resets (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL,                 -- 'all' | 'user'
    user_id    TEXT NULL REFERENCES users(id) ON DELETE SET NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_usage_resets_created ON usage_resets(created_at DESC);
