-- Keep the persisted DeepSeek catalog aligned with the official V4
-- Chat Completions models. DeepSeek's context cache is automatic and has no
-- cache-write charge, so only cache hits receive a separate input rate.

-- The model rows below carry a foreign key on providers(id). This migration
-- must not assume seeding created the provider: databases where the row was
-- deleted or never seeded (seedDefaults only runs on an empty plans table)
-- otherwise fail the whole migration with 23503 on every boot. Same defensive
-- pattern as 009_minimax_models.sql and 015_openrouter_provider.sql.
INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status)
VALUES ('deepseek', 'DeepSeek', '', 'https://api.deepseek.com', 'https://api.deepseek.com/anthropic', 'inactive')
ON CONFLICT (id) DO NOTHING;

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
        CASE WHEN NEW.muhiyacode_visible THEN ARRAY['muhiyacode']::TEXT[] ELSE ARRAY[]::TEXT[] END,
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

INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
    cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million,
    status, routing_tier, model_type, context_window, max_output_tokens,
    display_name, description, owned_by, supports_thinking, muhiyacode_visible
) VALUES (
    'model-deepseek-pro', 'deepseek-v4-pro', 'deepseek', 'deepseek-v4-pro',
    0.435, 0.87, 0.003625, 0,
    435000000, 870000000, 3625000, 0,
    'inactive', 'none', 'llm', 1000000, 384000,
    'DeepSeek V4 Pro', 'DeepSeek V4 Pro with thinking and non-thinking modes',
    'deepseek', TRUE, FALSE
)
ON CONFLICT DO NOTHING;

UPDATE models
   SET input_cost_per_million = 0.14,
       output_cost_per_million = 0.28,
       cache_read_cost_per_million = 0.0028,
       cache_write_cost_per_million = 0,
       input_cost_nano_usd_per_million = 140000000,
       output_cost_nano_usd_per_million = 280000000,
       cache_read_cost_nano_usd_per_million = 2800000,
       cache_write_cost_nano_usd_per_million = 0,
       context_window = 1000000,
       max_output_tokens = 384000,
       supports_thinking = TRUE
 WHERE provider_id = 'deepseek'
   AND target_model = 'deepseek-v4-flash';

UPDATE models
   SET input_cost_per_million = 0.435,
       output_cost_per_million = 0.87,
       cache_read_cost_per_million = 0.003625,
       cache_write_cost_per_million = 0,
       input_cost_nano_usd_per_million = 435000000,
       output_cost_nano_usd_per_million = 870000000,
       cache_read_cost_nano_usd_per_million = 3625000,
       cache_write_cost_nano_usd_per_million = 0,
       context_window = 1000000,
       max_output_tokens = 384000,
       supports_thinking = TRUE
 WHERE provider_id = 'deepseek'
   AND target_model = 'deepseek-v4-pro';

UPDATE model_catalog_metadata AS metadata
   SET provider_family = 'deepseek',
       cache_contract = '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":true}'::JSONB,
       supported_parameters = ARRAY[
           'model', 'messages', 'thinking', 'reasoning_effort', 'max_tokens',
           'response_format', 'stop', 'stream', 'stream_options', 'temperature',
           'top_p', 'tools', 'tool_choice', 'logprobs', 'top_logprobs', 'user_id'
       ]::TEXT[],
       updated_at = CURRENT_TIMESTAMP
  FROM models
 WHERE metadata.model_id = models.id
   AND models.provider_id = 'deepseek';
