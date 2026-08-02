-- SQL script to insert the deepseek-reasoner model (DeepSeek R1) into the models table
--
-- muhiyacode_visible is set explicitly rather than
-- left to its column default of FALSE: MuhiyaCode's model picker discovers
-- ONLY models with this flag set, regardless of status, so a raw-SQL insert
-- that omits it stays permanently absent from the coding agent's /model
-- selector even once activated via the admin panel's status toggle. The
-- model_catalog_metadata row (cache contract, provider family) is created
-- automatically by the models AFTER INSERT trigger; no companion insert is
-- needed here.

INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by,
    muhiyacode_visible, supports_thinking
) VALUES (
    'model-deepseek-r1',
    'deepseek-reasoner',
    'deepseek',
    'deepseek-v4-flash',
    0.14,
    0.28,
    0.0028,
    0.00,
    'inactive',
    'none',
    'llm',
    0.0,
    false,
    1000000,
    384000,
    'DeepSeek Reasoner (legacy alias)',
    'Legacy reasoning alias routed to DeepSeek V4 Flash',
    'deepseek',
    false,
    true
)
ON CONFLICT (id) DO NOTHING;
