-- 015_openrouter_provider.sql: additive OpenRouter provider + free Gemma vision.
--
-- OpenRouter is OpenAI-protocol, so it reuses the existing base_url dialect —
-- no schema change, no new adapter. Credentials are intentionally NOT seeded:
-- the provider and its models stay INACTIVE until an operator supplies an
-- OpenRouter API key (US card funds it) and enables them.
--
-- Gemma 4 31B is a free ($0) multimodal model; the Muhiya AI Router routes image
-- requests to it (the router's vision predicate matches "gemma"). Prices are 0.
--
-- Whisper-via-OpenRouter is NOT changed here to avoid breaking the currently
-- active OpenAI whisper. The cutover is a single operator UPDATE, documented in
-- gateway-ops-log.md, run only after this provider is active:
--   UPDATE models SET provider_id='openrouter', target_model='openai/whisper-1'
--   WHERE name='whisper-1';

INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status)
VALUES ('openrouter', 'OpenRouter', '', 'https://openrouter.ai/api/v1', '', 'inactive')
ON CONFLICT (id) DO NOTHING;

INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    status, routing_tier, model_type, context_window, max_output_tokens,
    display_name, description, owned_by
) VALUES
    ('model-gemma-4-vision', 'gemma-4-vision', 'openrouter', 'google/gemma-4-31b-it:free',
     0.00, 0.00, 0.00, 0.00,
     'inactive', 'none', 'llm', 256000, 0,
     'Gemma 4 31B', 'Free multimodal model (text + image) via OpenRouter', 'google')
ON CONFLICT (id) DO NOTHING;
