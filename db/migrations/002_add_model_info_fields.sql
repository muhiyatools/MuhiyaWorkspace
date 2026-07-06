-- 002_add_model_info_fields.sql: Add context window, max output tokens, display name, description, and owned_by to models table.
ALTER TABLE models ADD COLUMN IF NOT EXISTS context_window INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN IF NOT EXISTS max_output_tokens INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN IF NOT EXISTS display_name VARCHAR(255) DEFAULT '';
ALTER TABLE models ADD COLUMN IF NOT EXISTS description TEXT DEFAULT '';
ALTER TABLE models ADD COLUMN IF NOT EXISTS owned_by VARCHAR(100) DEFAULT '';

-- Seed/Update defaults for existing base models
UPDATE models SET context_window = 128000, max_output_tokens = 4096, display_name = 'GPT-4o', owned_by = 'openai', description = 'OpenAI flagship model' WHERE name = 'gpt-4o' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 200000, max_output_tokens = 8192, display_name = 'Claude 3.5 Sonnet', owned_by = 'anthropic', description = 'Anthropic high-intelligence model' WHERE name = 'claude-3-5-sonnet' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 200000, max_output_tokens = 8192, display_name = 'Claude 3.7 Sonnet', owned_by = 'anthropic', description = 'Anthropic latest model' WHERE name = 'claude-3-7-sonnet' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 64000, max_output_tokens = 8192, display_name = 'DeepSeek Chat', owned_by = 'deepseek', description = 'DeepSeek cheap general-purpose model' WHERE name = 'deepseek-chat' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 64000, max_output_tokens = 8192, display_name = 'DeepSeek Reasoner', owned_by = 'deepseek', description = 'DeepSeek reasoning model (R1)' WHERE name = 'deepseek-reasoner' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 64000, max_output_tokens = 8192, display_name = 'DeepSeek v4 Flash', owned_by = 'deepseek', description = 'DeepSeek flash model' WHERE name = 'deepseek-v4-flash' AND (context_window IS NULL OR context_window = 0);
UPDATE models SET context_window = 0, max_output_tokens = 0, display_name = 'Whisper 1', owned_by = 'openai', description = 'OpenAI speech-to-text model' WHERE name = 'whisper-1' AND (context_window IS NULL OR context_window = 0);
