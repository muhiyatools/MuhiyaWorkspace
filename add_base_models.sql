-- SQL script to insert the full base models including GPT-4o, Claude 3.5/3.7, and DeepSeek models

-- 1. Ensure providers exist
INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES
('openai', 'OpenAI', '', 'https://api.openai.com/v1', '', 'inactive'),
('openrouter', 'OpenRouter', '', 'https://openrouter.ai/api/v1', '', 'inactive'),
('anthropic', 'Anthropic', '', '', 'https://api.anthropic.com', 'inactive'),
('deepseek', 'DeepSeek', '', 'https://api.deepseek.com', 'https://api.deepseek.com/anthropic', 'inactive')
ON CONFLICT (id) DO NOTHING;

-- 2. Insert missing models
--
-- muhiyacode_visible is set explicitly (matching status) rather than left to
-- its column default of FALSE: MuhiyaCode's model picker discovers ONLY
-- models with this flag set, regardless of status, so a raw-SQL insert that
-- omits it stays permanently absent from the coding agent's /model selector
-- even once activated via the admin panel's status toggle. The
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
) VALUES
('model-gpt4o', 'gpt-4o', 'openai', 'gpt-4o', 2.50, 10.00, 1.25, 2.50, 'inactive', 'none', 'llm', 0.0, false, 128000, 4096, 'GPT-4o', 'OpenAI flagship model', 'openai', false, false),
('model-claude-3-5', 'claude-3-5-sonnet', 'anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 0.30, 3.75, 'inactive', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.5 Sonnet', 'Anthropic high-intelligence model', 'anthropic', false, false),
('model-claude-3-7', 'claude-3-7-sonnet', 'anthropic', 'claude-3-7-sonnet-20250219', 3.00, 15.00, 0.30, 3.75, 'inactive', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.7 Sonnet', 'Anthropic latest model', 'anthropic', false, false),
-- target_model is sent upstream as the model name. `deepseek-chat` was retired
-- on 2026-07-24; it resolved to deepseek-v4-flash, which is what this targets.
('model-deepseek-chat', 'deepseek-chat', 'deepseek', 'deepseek-v4-flash', 0.14, 0.28, 0.0028, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek Chat (legacy alias)', 'Legacy alias routed to DeepSeek V4 Flash', 'deepseek', false, true),
('model-deepseek-flash', 'deepseek-v4-flash', 'deepseek', 'deepseek-v4-flash', 0.14, 0.28, 0.0028, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek V4 Flash', 'DeepSeek V4 Flash with thinking and non-thinking modes', 'deepseek', false, true),
('model-deepseek-pro', 'deepseek-v4-pro', 'deepseek', 'deepseek-v4-pro', 0.435, 0.87, 0.003625, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek V4 Pro', 'DeepSeek V4 Pro with thinking and non-thinking modes', 'deepseek', false, true),
('model-whisper', 'whisper-1', 'openrouter', 'openai/whisper-1', 0.00, 0.00, 0.00, 0.00, 'inactive', 'none', 'transcript', 0.006, true, 0, 0, 'Whisper 1', 'OpenAI speech-to-text model via OpenRouter', 'openai', false, false)
ON CONFLICT (id) DO NOTHING;
