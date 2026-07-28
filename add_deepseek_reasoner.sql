-- SQL script to insert the deepseek-reasoner model (DeepSeek R1) into the models table

INSERT INTO models (
    id, name, provider_id, target_model, 
    input_cost_per_million, output_cost_per_million, 
    cache_read_cost_per_million, cache_write_cost_per_million, 
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by
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
    'deepseek'
)
ON CONFLICT (id) DO NOTHING;
