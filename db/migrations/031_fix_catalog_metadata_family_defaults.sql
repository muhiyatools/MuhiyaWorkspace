-- 031: fix ensure_model_catalog_metadata() (026) to derive provider_family,
-- cache_contract, and supported_parameters by model name, matching 026's own
-- one-time backfill SELECT. The trigger as written hardcoded
-- provider_family='openai-compatible' and left cache_contract/
-- supported_parameters at their bare column defaults ('{}'::JSONB / empty
-- array) for every row inserted since 026 landed — so a DeepSeek or Minimax
-- model added via raw SQL (every add_*.sql/seed.sql/setup_database.sql script
-- in this repo does exactly that, and none of them set these columns) got
-- catalog metadata that does not describe its real prompt-cache behavior:
-- MuhiyaCode reads exactly this cache_contract to decide whether a model is
-- cache-capable at all.
--
-- This migration replaces the trigger function so future inserts are correct,
-- then repairs existing rows the buggy trigger already created — matched by
-- name, and guarded so a row an operator already corrected via the admin
-- panel (which always derives these fields correctly) is left untouched.

CREATE OR REPLACE FUNCTION ensure_model_catalog_metadata()
RETURNS TRIGGER AS $$
DECLARE
    v_provider_family VARCHAR(64);
    v_cache_contract JSONB;
BEGIN
    IF NEW.name = 'muhiya-ai-router' THEN
        RETURN NEW;
    END IF;
    v_provider_family := CASE
        WHEN lower(NEW.name) LIKE 'minimax%' THEN 'minimax-openrouter'
        WHEN lower(NEW.name) LIKE 'deepseek%' THEN 'deepseek'
        ELSE 'openai-compatible'
    END;
    v_cache_contract := CASE
        WHEN v_provider_family = 'minimax-openrouter' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":true,"supports_prompt_cache":true}'::JSONB
        WHEN v_provider_family = 'deepseek' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":true}'::JSONB
        ELSE
            '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":false}'::JSONB
    END;
    INSERT INTO model_catalog_metadata (
        model_id, tags, provider_family, cache_contract, supported_parameters,
        pricing_rule_set_id, health
    ) VALUES (
        NEW.id,
        CASE WHEN NEW.muhiyacode_visible THEN ARRAY['muhiyacode']::TEXT[] ELSE ARRAY[]::TEXT[] END,
        v_provider_family,
        v_cache_contract,
        ARRAY['model','messages','stream','tools','tool_choice','max_tokens','temperature','top_p']::TEXT[],
        'pricing:' || NEW.id,
        CASE WHEN NEW.status = 'active' THEN 'healthy' ELSE 'unavailable' END
    )
    ON CONFLICT (model_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Repair rows the pre-031 trigger already created with the wrong generic
-- defaults. Only rows still sitting at the buggy default are touched.
UPDATE model_catalog_metadata mm
SET provider_family = CASE
        WHEN lower(m.name) LIKE 'minimax%' THEN 'minimax-openrouter'
        WHEN lower(m.name) LIKE 'deepseek%' THEN 'deepseek'
        ELSE mm.provider_family
    END,
    cache_contract = CASE
        WHEN lower(m.name) LIKE 'minimax%' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":true,"supports_prompt_cache":true}'::JSONB
        WHEN lower(m.name) LIKE 'deepseek%' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":true}'::JSONB
        ELSE mm.cache_contract
    END,
    supported_parameters = CASE
        WHEN mm.supported_parameters = ARRAY[]::TEXT[] THEN
            ARRAY['model','messages','stream','tools','tool_choice','max_tokens','temperature','top_p']::TEXT[]
        ELSE mm.supported_parameters
    END,
    updated_at = CURRENT_TIMESTAMP
FROM models m
WHERE mm.model_id = m.id
  AND mm.provider_family = 'openai-compatible'
  AND (lower(m.name) LIKE 'minimax%' OR lower(m.name) LIKE 'deepseek%');
