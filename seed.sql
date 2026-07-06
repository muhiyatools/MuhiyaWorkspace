-- =========================================================================
-- seed.sql: Base models, plans, system settings, and providers for MuhiyaLLM
-- =========================================================================

-- 1. System Settings
INSERT INTO system_settings (key, value) VALUES 
('gateway_name', 'MuhiyaLLM Gateway'),
('theme_accent', 'emerald'),
('seeded_defaults', 'true')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

-- 2. Base Plans
INSERT INTO plans (id, name, rpm_limit, tpm_limit) VALUES 
('plan-dev', 'MuhiyaCode Free', 30, 300000),
('yalla', 'MuhiyaCode Yalla', 60, 1200000),
('max', 'MuhiyaCode Max', 100, 2500000)
ON CONFLICT (id) DO NOTHING;

-- 3. Budget Windows
INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) VALUES
('budget-plan-dev-5h', 'plan-dev', 'Short Term (5 Hours)', 18000, 0.50),
('budget-yalla-5h', 'yalla', 'Short Term (5 Hours)', 18000, 25.00),
('budget-yalla-31d', 'yalla', 'Monthly Budget (31 Days)', 2678400, 25.00),
('budget-max-5h', 'max', 'Short Term (5 Hours)', 18000, 50.00),
('budget-max-31d', 'max', 'Monthly Budget (31 Days)', 2678400, 50.00)
ON CONFLICT (id) DO NOTHING;

-- 4. Providers
INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES
('openai', 'OpenAI', 'mock-openai-key', 'https://api.openai.com/v1', '', 'active'),
('anthropic', 'Anthropic', 'mock-anthropic-key', '', 'https://api.anthropic.com', 'active'),
('deepseek', 'DeepSeek', 'mock-deepseek-key', 'https://api.deepseek.com', 'https://api.deepseek.com/anthropic', 'active')
ON CONFLICT (id) DO NOTHING;

-- 5. Base Models
INSERT INTO models (
    id, name, provider_id, target_model, 
    input_cost_per_million, output_cost_per_million, 
    cache_read_cost_per_million, cache_write_cost_per_million, 
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by
) VALUES
('model-gpt4o', 'gpt-4o', 'openai', 'gpt-4o', 2.50, 10.00, 1.25, 2.50, 'active', 'none', 'llm', 0.0, false, 128000, 4096, 'GPT-4o', 'OpenAI flagship model', 'openai'),
('model-claude', 'claude-3-5-sonnet', 'anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 0.30, 3.75, 'active', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.5 Sonnet', 'Anthropic high-intelligence model', 'anthropic'),
('model-deepseek', 'deepseek-chat', 'deepseek', 'deepseek-chat', 0.14, 0.28, 0.07, 0.14, 'active', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek Chat', 'DeepSeek cheap general-purpose model', 'deepseek'),
('model-deepseek-flash', 'deepseek-v4-flash', 'deepseek', 'deepseek-chat', 0.14, 0.28, 0.07, 0.14, 'active', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek v4 Flash', 'DeepSeek flash model', 'deepseek'),
('model-whisper', 'whisper-1', 'openai', 'whisper-1', 0.00, 0.00, 0.00, 0.00, 'active', 'none', 'transcript', 0.006, true, 0, 0, 'Whisper 1', 'OpenAI speech-to-text model', 'openai')
ON CONFLICT (id) DO NOTHING;

-- =========================================================================
-- OPTIONAL: Seed a Base User and a Virtual Key
-- Uncomment the queries below if you want to create a default active user 
-- and API key immediately.
-- =========================================================================

-- INSERT INTO users (id, name, email, plan_id, status) VALUES
-- ('usr-admin', 'Admin User', 'admin@muhiya.local', 'plan-dev', 'active')
-- ON CONFLICT (id) DO NOTHING;

-- INSERT INTO virtual_keys (id, name, user_id, status, expires_at) VALUES
-- ('key-admin-test-12345', 'Default Admin Key', 'usr-admin', 'active', NULL)
-- ON CONFLICT (id) DO NOTHING;
