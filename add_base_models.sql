-- SQL script to insert the full base models including GPT-4o, Claude 3.5/3.7, and DeepSeek models

-- 1. Ensure providers exist
INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES
('openai', 'OpenAI', 'mock-openai-key', 'https://api.openai.com/v1', '', 'active'),
('anthropic', 'Anthropic', 'mock-anthropic-key', '', 'https://api.anthropic.com', 'active'),
('deepseek', 'DeepSeek', 'mock-deepseek-key', 'https://api.deepseek.com', 'https://api.deepseek.com/anthropic', 'active')
ON CONFLICT (id) DO NOTHING;

-- 2. Insert missing models
INSERT INTO models (
    id, name, provider_id, target_model, 
    input_cost_per_million, output_cost_per_million, 
    cache_read_cost_per_million, cache_write_cost_per_million, 
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by
) VALUES 
('model-gpt4o', 'gpt-4o', 'openai', 'gpt-4o', 2.50, 10.00, 1.25, 2.50, 'active', 'none', 'llm', 0.0, false, 128000, 4096, 'GPT-4o', 'OpenAI flagship model', 'openai'),
('model-claude-3-5', 'claude-3-5-sonnet', 'anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 0.30, 3.75, 'active', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.5 Sonnet', 'Anthropic high-intelligence model', 'anthropic'),
('model-claude-3-7', 'claude-3-7-sonnet', 'anthropic', 'claude-3-7-sonnet-20250219', 3.00, 15.00, 0.30, 3.75, 'active', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.7 Sonnet', 'Anthropic latest model', 'anthropic'),
('model-deepseek-chat', 'deepseek-chat', 'deepseek', 'deepseek-chat', 0.14, 0.28, 0.07, 0.14, 'active', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek Chat', 'DeepSeek cheap general-purpose model', 'deepseek')
ON CONFLICT (id) DO NOTHING;
