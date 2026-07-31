-- 033_model_cache_ttl_rates.sql
--
-- Providers increasingly price cache writes and reads by how long the entry
-- lives: a five-minute ephemeral write costs less than a one-hour one. The
-- models table carries a single cache-read and single cache-write rate, which
-- cannot express that.
--
-- A NULL rate here means "inherit the rate resolved from the model or its
-- context tier". That inheritance is what guarantees every model that has no
-- rows in this table prices exactly as it did before this migration.

CREATE TABLE IF NOT EXISTS model_cache_ttl_rates (
    id          VARCHAR(100) PRIMARY KEY,
    model_id    VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    -- NULL pairs these rates with the model's base rates. A value pairs them
    -- with the model_pricing_tiers row at the same threshold, so a provider
    -- can charge more for long-TTL cache writes on large contexts.
    min_input_tokens_exclusive BIGINT,
    ttl         VARCHAR(16) NOT NULL,
    cache_read_nano_usd_per_million  BIGINT,
    cache_write_nano_usd_per_million BIGINT,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (ttl IN ('5m', '1h', 'default')),
    CHECK (min_input_tokens_exclusive IS NULL OR min_input_tokens_exclusive >= 0),
    CHECK (cache_read_nano_usd_per_million  IS NULL OR cache_read_nano_usd_per_million  >= 0),
    CHECK (cache_write_nano_usd_per_million IS NULL OR cache_write_nano_usd_per_million >= 0)
);

-- A partial unique index rather than a table constraint: NULL thresholds must
-- collide with each other, and a plain UNIQUE treats every NULL as distinct.
CREATE UNIQUE INDEX IF NOT EXISTS idx_model_cache_ttl_rates_base_unique
    ON model_cache_ttl_rates (model_id, ttl)
    WHERE min_input_tokens_exclusive IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_cache_ttl_rates_tier_unique
    ON model_cache_ttl_rates (model_id, min_input_tokens_exclusive, ttl)
    WHERE min_input_tokens_exclusive IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_model_cache_ttl_rates_model
    ON model_cache_ttl_rates (model_id, enabled);
