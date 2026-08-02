-- 038_model_muhiyachat_visible.sql: per-model discoverability for MuhiyaChat.
--
-- The mirror of 021's muhiyacode_visible. Until now MuhiyaChat saw the entire
-- active catalog, so a model published purely for the coding agent (or one an
-- operator is still testing) had no way to be kept out of the chat model picker
-- short of deactivating it for everyone.
--
-- Same semantics as the MuhiyaCode flag, deliberately:
--   * It gates DISCOVERY only. Inference by exact name is never gated, so a
--     hidden model stays fully usable — an existing conversation pinned to it
--     keeps working rather than breaking the moment an operator unchecks a box.
--   * The column is the single visibility authority. The 'muhiyachat' catalog
--     tag below is derived FROM it, never the other way round, so a direct SQL
--     UPDATE can never leave the two disagreeing (the drift that catalog.go
--     documents for the MuhiyaCode tag).
--
-- Default FALSE gives opt-in semantics for all FUTURE models. The backfill makes
-- THIS migration behavior-neutral at deploy: every row MuhiyaChat could already
-- discover is marked visible, so operators only ever need to UNcheck. The
-- discovery filter lists a model when it is status='active' AND NOT transcribe
-- and does not consult model_type, so the backfill matches exactly NOT
-- transcribe (any status) — re-activating an old row later restores its prior
-- discoverability too.

ALTER TABLE models ADD COLUMN IF NOT EXISTS muhiyachat_visible BOOLEAN NOT NULL DEFAULT false;

UPDATE models
   SET muhiyachat_visible = true
 WHERE COALESCE(transcribe, false) = false;

-- Carry the flag into model_catalog_metadata.tags, alongside 'muhiyacode'.
-- Unchanged from 037 except for the tags expression.
CREATE OR REPLACE FUNCTION ensure_model_catalog_metadata()
RETURNS TRIGGER AS $$
DECLARE
    v_provider_family VARCHAR(64);
    v_cache_contract JSONB;
    v_supported_parameters TEXT[];
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
    v_supported_parameters := CASE
        WHEN v_provider_family = 'deepseek' THEN ARRAY[
            'model', 'messages', 'thinking', 'reasoning_effort', 'max_tokens',
            'response_format', 'stop', 'stream', 'stream_options', 'temperature',
            'top_p', 'tools', 'tool_choice', 'logprobs', 'top_logprobs', 'user_id'
        ]::TEXT[]
        ELSE ARRAY[
            'model', 'messages', 'stream', 'tools', 'tool_choice', 'max_tokens',
            'temperature', 'top_p'
        ]::TEXT[]
    END;
    INSERT INTO model_catalog_metadata (
        model_id, tags, provider_family, cache_contract, supported_parameters,
        pricing_rule_set_id, health
    ) VALUES (
        NEW.id,
        (CASE WHEN NEW.muhiyacode_visible THEN ARRAY['muhiyacode']::TEXT[] ELSE ARRAY[]::TEXT[] END)
          || (CASE WHEN NEW.muhiyachat_visible THEN ARRAY['muhiyachat']::TEXT[] ELSE ARRAY[]::TEXT[] END),
        v_provider_family,
        v_cache_contract,
        v_supported_parameters,
        'pricing:' || NEW.id,
        CASE WHEN NEW.status = 'active' THEN 'healthy' ELSE 'unavailable' END
    )
    ON CONFLICT (model_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
