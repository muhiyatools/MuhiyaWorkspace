-- 034_model_price_windows.sql
--
-- Time-of-day pricing: some providers charge a multiple of their normal rate
-- during peak hours (DeepSeek's peak/off-peak schedule being the motivating
-- case). This is modelled as a multiplier over whatever the base rates and
-- context tier already resolved to, not as a second set of absolute rates:
-- a provider that doubles everything during peak needs one row, not one row
-- per tier per token class.
--
-- The multiplier is an exact rational rather than a float or a percentage so
-- that "2x" is exactly 2x, with no rounding drift accumulating across a
-- month of requests.
--
-- All boundaries are UTC minutes-of-day, which removes any daylight-saving
-- ambiguity: providers publish these schedules in UTC and we evaluate them
-- in UTC. start > end means the window wraps midnight, which off-peak
-- schedules routinely do.

CREATE TABLE IF NOT EXISTS model_price_windows (
    id       VARCHAR(100) PRIMARY KEY,
    model_id VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    label    VARCHAR(64) NOT NULL DEFAULT '',

    -- [start, end). start > end wraps midnight, e.g. 16:30 -> 00:30.
    start_minute_utc INTEGER NOT NULL,
    end_minute_utc   INTEGER NOT NULL,

    -- Bitmask of ISO weekdays, Monday = bit 0 (value 1). 127 = every day.
    weekday_mask INTEGER NOT NULL DEFAULT 127,

    multiplier_num BIGINT NOT NULL,
    multiplier_den BIGINT NOT NULL DEFAULT 1,

    -- Comma-separated token classes this scale applies to; empty = all of
    -- them. Lets a provider raise output prices at peak without touching
    -- cache reads.
    applies_to TEXT NOT NULL DEFAULT '',

    -- Exactly one window applies to a request: the highest priority match.
    -- Windows deliberately do not stack, because compounding multipliers
    -- makes a charge impossible to explain and one bad row could silently
    -- quadruple a bill.
    priority INTEGER NOT NULL DEFAULT 0,

    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from  TIMESTAMP WITH TIME ZONE,
    effective_until TIMESTAMP WITH TIME ZONE,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CHECK (start_minute_utc >= 0 AND start_minute_utc < 1440),
    CHECK (end_minute_utc   >= 0 AND end_minute_utc  <= 1440),
    -- Equal boundaries are ambiguous (empty day or whole day?); an explicit
    -- 0..1440 says "all day" without guessing.
    CHECK (start_minute_utc <> end_minute_utc),
    CHECK (weekday_mask >= 1 AND weekday_mask <= 127),
    CHECK (multiplier_num >= 0),
    CHECK (multiplier_den >  0),
    CHECK (effective_until IS NULL OR effective_from IS NULL
           OR effective_until > effective_from)
);

CREATE INDEX IF NOT EXISTS idx_model_price_windows_model_enabled
    ON model_price_windows (model_id, enabled, priority DESC);
