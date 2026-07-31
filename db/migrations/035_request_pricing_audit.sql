-- 035_request_pricing_audit.sql
--
-- request_logs previously stored tokens and one final cost_nano_usd, with no
-- record of which rates produced that number. The rule-set hash was computed
-- on every request and then discarded. That makes a charge recorded but not
-- auditable: there is no way to answer "was this billed at peak?", "which
-- context tier fired?", or "the price was edited last week, what was this
-- request actually billed at?".
--
-- These columns plus request_pricing_lines are the derivation of every charge.
-- Given a receipt alone the cost can be recomputed without consulting the
-- models table at all, which is what makes historical invoices defensible.

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS pricing_rule_set_id    VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pricing_tier_threshold BIGINT,
    ADD COLUMN IF NOT EXISTS price_window_id        VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS price_multiplier_num   BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS price_multiplier_den   BIGINT NOT NULL DEFAULT 1,
    -- The instant pricing was pinned to, captured at admission. Settlement
    -- re-uses it rather than reading the clock, so a request that straddles a
    -- peak-pricing boundary is billed at the rate it was quoted.
    ADD COLUMN IF NOT EXISTS priced_at              TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS prompt_accounting      VARCHAR(16) NOT NULL DEFAULT 'inclusive',
    -- The upstream's own reported cost where it provides one. Divergence from
    -- cost_nano_usd means our catalog price has drifted from the provider's
    -- real one — which is otherwise discovered only by accident.
    ADD COLUMN IF NOT EXISTS upstream_cost_nano_usd BIGINT;

ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_multiplier_valid,
    ADD CONSTRAINT request_logs_multiplier_valid
        CHECK (price_multiplier_num >= 0 AND price_multiplier_den > 0),
    DROP CONSTRAINT IF EXISTS request_logs_upstream_cost_nonnegative,
    ADD CONSTRAINT request_logs_upstream_cost_nonnegative
        CHECK (upstream_cost_nano_usd IS NULL OR upstream_cost_nano_usd >= 0);

CREATE INDEX IF NOT EXISTS idx_request_logs_price_window
    ON request_logs (price_window_id, created_at DESC)
    WHERE price_window_id <> '';

CREATE TABLE IF NOT EXISTS request_pricing_lines (
    request_log_id VARCHAR(100) NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
    token_class    VARCHAR(32)  NOT NULL,
    tokens         BIGINT       NOT NULL,
    -- The catalogue rate before the time-window multiplier, so a receipt shows
    -- both the list price and what was actually charged.
    rate_nano_usd_per_million BIGINT NOT NULL,
    cost_nano_usd  BIGINT       NOT NULL,
    PRIMARY KEY (request_log_id, token_class),
    CHECK (tokens >= 0),
    CHECK (rate_nano_usd_per_million >= 0),
    CHECK (cost_nano_usd >= 0),
    CHECK (token_class IN (
        'input_fresh', 'output',
        'cache_read', 'cache_read_5m',
        'cache_write', 'cache_write_5m', 'cache_write_1h'
    ))
);

-- Answers "how much of this month went to cache writes vs fresh input?"
-- without a sequential scan of every request.
CREATE INDEX IF NOT EXISTS idx_request_pricing_lines_class
    ON request_pricing_lines (token_class);
