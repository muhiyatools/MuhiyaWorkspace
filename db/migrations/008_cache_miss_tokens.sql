-- 008_cache_miss_tokens.sql: record the provider-reported cache-miss (non-cached
-- prompt) token count alongside cache_read_tokens, so the dashboard hit-rate can
-- use read/(read+miss) instead of approximating the denominator from input_tokens.
-- Nullable with no backfill: legacy rows stay NULL and fall back to the old
-- input-based approximation.
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cache_miss_tokens BIGINT;
