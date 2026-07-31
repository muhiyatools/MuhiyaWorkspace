-- 032_prompt_accounting.sql
--
-- Providers disagree about what "prompt tokens" means, and the pricing engine
-- previously assumed one convention for all of them.
--
--   * OpenAI, DeepSeek and the OpenRouter OpenAI dialect report prompt_tokens
--     INCLUDING tokens served from or written to cache.
--   * Anthropic reports usage.input_tokens EXCLUDING cache_read_input_tokens
--     and cache_creation_input_tokens.
--
-- Pricing subtracted cached tokens from the reported prompt total in every
-- case. For Anthropic-family models that subtracted tokens which were never in
-- the total, clamped the remainder at zero, and billed genuinely fresh input
-- at nothing. On a warm cache that is a silent undercharge on every turn.
--
-- This column tells the pricing engine which convention a model speaks. It
-- stops the undercharge going forward; it does not retro-bill past requests,
-- which is a business decision rather than a schema one.

ALTER TABLE models
    ADD COLUMN IF NOT EXISTS prompt_accounting VARCHAR(16) NOT NULL DEFAULT 'inclusive';

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_prompt_accounting_valid;
ALTER TABLE models ADD CONSTRAINT models_prompt_accounting_valid
    CHECK (prompt_accounting IN ('inclusive', 'exclusive'));

-- Anthropic-family models are the exclusive-accounting case. Both the catalog
-- metadata family and the cache contract are consulted because a model may
-- carry either marker depending on how it was registered.
UPDATE models
   SET prompt_accounting = 'exclusive'
 WHERE id IN (
    SELECT m.id
      FROM models AS m
      LEFT JOIN model_catalog_metadata AS meta ON meta.model_id = m.id
     WHERE lower(COALESCE(meta.provider_family, '')) = 'anthropic'
        OR COALESCE(meta.cache_contract::text, '') LIKE '%anthropic%'
 );

-- Surfaces upstreams whose own usage numbers were internally inconsistent
-- (for example an inclusive provider reporting fewer prompt tokens than it
-- claimed to serve from cache). Pricing clamps such values, but the clamp is
-- now recorded rather than silently absorbed.
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS usage_anomaly VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_request_logs_usage_anomaly
    ON request_logs (usage_anomaly, created_at DESC)
    WHERE usage_anomaly <> '';
