-- 009_minimax_models.sql: additive MiniMax OpenAI-provider catalog.
--
-- Production credentials are intentionally not seeded. The provider and its
-- models remain inactive until an operator supplies a MiniMax API key and
-- enables them. Prices are USD per million tokens; MiniMax-M3's >512k tier is
-- applied by the request cost helper because it depends on prompt size.
INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status)
VALUES ('minimax', 'MiniMax', '', 'https://api.minimax.io/v1', '', 'inactive')
ON CONFLICT (id) DO NOTHING;

INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    status, routing_tier, model_type, context_window, max_output_tokens,
    display_name, description, owned_by
) VALUES
    ('model-minimax-m3', 'minimax-m3', 'minimax', 'MiniMax-M3', 0.30, 1.20, 0.06, 0.00, 'inactive', 'hard', 'llm', 1000000, 0, 'MiniMax M3', 'MiniMax frontier multimodal coding model', 'minimax'),
    ('model-minimax-m2-7', 'minimax-m2.7', 'minimax', 'MiniMax-M2.7', 0.30, 1.20, 0.06, 0.00, 'inactive', 'hard', 'llm', 204800, 0, 'MiniMax M2.7', 'MiniMax M2.7 reasoning model', 'minimax'),
    ('model-minimax-m2-7-highspeed', 'minimax-m2.7-highspeed', 'minimax', 'MiniMax-M2.7-highspeed', 0.60, 2.40, 0.06, 0.00, 'inactive', 'hard', 'llm', 204800, 0, 'MiniMax M2.7 Highspeed', 'MiniMax M2.7 high-speed reasoning model', 'minimax'),
    ('model-minimax-m2-5', 'minimax-m2.5', 'minimax', 'MiniMax-M2.5', 0.30, 1.20, 0.03, 0.00, 'inactive', 'medium', 'llm', 204800, 0, 'MiniMax M2.5', 'MiniMax M2.5 reasoning model', 'minimax'),
    ('model-minimax-m2-1', 'minimax-m2.1', 'minimax', 'MiniMax-M2.1', 0.30, 1.20, 0.03, 0.00, 'inactive', 'medium', 'llm', 204800, 0, 'MiniMax M2.1', 'MiniMax M2.1 reasoning model', 'minimax'),
    ('model-minimax-m2', 'minimax-m2', 'minimax', 'MiniMax-M2', 0.30, 1.20, 0.03, 0.00, 'inactive', 'medium', 'llm', 204800, 0, 'MiniMax M2', 'MiniMax M2 reasoning model', 'minimax')
ON CONFLICT (id) DO NOTHING;
