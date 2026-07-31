-- SQL script to insert the deepseek-reasoner model (DeepSeek R1) into the models table
--
-- muhiyacode_visible is set explicitly (matching status='active') rather than
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
    muhiyacode_visible
) VALUES (
    'model-deepseek-r1',
    'deepseek-reasoner',
    'deepseek',
    'deepseek-reasoner',
    0.55,
    2.19,
    0.14,
    0.55,
    'inactive',
    'none',
    'llm',
    0.0,
    false,
    64000,
    8192,
    'DeepSeek Reasoner',
    'DeepSeek reasoning model (R1)',
    'deepseek',
    false
)
ON CONFLICT (id) DO NOTHING;
