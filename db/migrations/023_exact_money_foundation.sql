-- 023_exact_money_foundation.sql
-- Add exact nano-USD storage alongside the legacy DOUBLE PRECISION columns.
-- One USD = 1,000,000,000 nano-USD. During the compatibility window the Go
-- layer dual-writes both representations; financial authorization reads only
-- the integer columns after their parity checks pass.

ALTER TABLE budget_windows
    ADD COLUMN IF NOT EXISTS budget_nano_usd BIGINT NOT NULL DEFAULT 0;
UPDATE budget_windows
   SET budget_nano_usd = ROUND(budget_usd * 1000000000.0)::BIGINT
 WHERE budget_nano_usd = 0 AND budget_usd <> 0;

ALTER TABLE models
    ADD COLUMN IF NOT EXISTS input_cost_nano_usd_per_million BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS output_cost_nano_usd_per_million BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_read_cost_nano_usd_per_million BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_write_cost_nano_usd_per_million BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS price_per_minute_nano_usd BIGINT NOT NULL DEFAULT 0;
UPDATE models
   SET input_cost_nano_usd_per_million = ROUND(input_cost_per_million * 1000000000.0)::BIGINT,
       output_cost_nano_usd_per_million = ROUND(output_cost_per_million * 1000000000.0)::BIGINT,
       cache_read_cost_nano_usd_per_million = ROUND(cache_read_cost_per_million * 1000000000.0)::BIGINT,
       cache_write_cost_nano_usd_per_million = ROUND(cache_write_cost_per_million * 1000000000.0)::BIGINT,
       price_per_minute_nano_usd = ROUND(COALESCE(price_per_minute, 0) * 1000000000.0)::BIGINT
 WHERE input_cost_nano_usd_per_million = 0
    OR output_cost_nano_usd_per_million = 0
    OR cache_read_cost_nano_usd_per_million = 0
    OR cache_write_cost_nano_usd_per_million = 0
    OR price_per_minute_nano_usd = 0;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS cost_nano_usd BIGINT NOT NULL DEFAULT 0;
UPDATE request_logs
   SET cost_nano_usd = ROUND(cost * 1000000000.0)::BIGINT
 WHERE cost_nano_usd = 0 AND cost <> 0;

ALTER TABLE user_topups
    ADD COLUMN IF NOT EXISTS amount_nano_usd BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS used_nano_usd BIGINT NOT NULL DEFAULT 0;
UPDATE user_topups
   SET amount_nano_usd = ROUND(credits * 10000000.0)::BIGINT,
       used_nano_usd = ROUND(used_credits * 10000000.0)::BIGINT
 WHERE (amount_nano_usd = 0 AND credits <> 0)
    OR (used_nano_usd = 0 AND used_credits <> 0);

ALTER TABLE user_window_charge_state
    ADD COLUMN IF NOT EXISTS last_billed_nano_usd BIGINT NOT NULL DEFAULT 0;
UPDATE user_window_charge_state
   SET last_billed_nano_usd = ROUND(last_billed_spend * 1000000000.0)::BIGINT
 WHERE last_billed_nano_usd = 0 AND last_billed_spend <> 0;

CREATE TABLE IF NOT EXISTS budget_reservations (
    id VARCHAR(100) PRIMARY KEY,
    request_id VARCHAR(100) NOT NULL UNIQUE,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    virtual_key_id VARCHAR(100) REFERENCES virtual_keys(id) ON DELETE SET NULL,
    model_id VARCHAR(100) REFERENCES models(id) ON DELETE SET NULL,
    amount_nano_usd BIGINT NOT NULL,
    settled_nano_usd BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL CHECK (status IN ('reserved', 'settled', 'released', 'expired')),
    price_snapshot_id VARCHAR(100) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (amount_nano_usd >= 0),
    CHECK (settled_nano_usd >= 0),
    CHECK (settled_nano_usd <= amount_nano_usd)
);

CREATE INDEX IF NOT EXISTS idx_budget_reservations_user_active
    ON budget_reservations(user_id, status, lease_expires_at);

CREATE TABLE IF NOT EXISTS account_ledger (
    id VARCHAR(100) PRIMARY KEY,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id VARCHAR(100),
    reservation_id VARCHAR(100) REFERENCES budget_reservations(id) ON DELETE SET NULL,
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('debit', 'credit', 'refund', 'adjustment')),
    amount_nano_usd BIGINT NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE budget_windows DROP CONSTRAINT IF EXISTS budget_windows_budget_nano_nonnegative;
ALTER TABLE budget_windows ADD CONSTRAINT budget_windows_budget_nano_nonnegative CHECK (budget_nano_usd >= 0);
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_exact_prices_nonnegative;
ALTER TABLE models ADD CONSTRAINT models_exact_prices_nonnegative CHECK (
    input_cost_nano_usd_per_million >= 0 AND
    output_cost_nano_usd_per_million >= 0 AND
    cache_read_cost_nano_usd_per_million >= 0 AND
    cache_write_cost_nano_usd_per_million >= 0 AND
    price_per_minute_nano_usd >= 0
);
ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_cost_nano_nonnegative;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_cost_nano_nonnegative CHECK (cost_nano_usd >= 0);
ALTER TABLE user_topups DROP CONSTRAINT IF EXISTS user_topups_exact_amounts_valid;
ALTER TABLE user_topups ADD CONSTRAINT user_topups_exact_amounts_valid CHECK (
    amount_nano_usd >= 0 AND used_nano_usd >= 0 AND used_nano_usd <= amount_nano_usd
);
