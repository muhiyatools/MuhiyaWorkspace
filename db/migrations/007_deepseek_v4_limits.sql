-- 007_deepseek_v4_limits.sql: Update documented DeepSeek model limits to the
-- 2026-07-14 values (context_window 1,000,000; max_output_tokens 384,000).
-- Metadata only - covers the base chat/reasoner models and every deepseek-v4-*
-- alias row. Column names match the models schema from 002_add_model_info_fields.
UPDATE models SET context_window = 1000000, max_output_tokens = 384000 WHERE name = 'deepseek-chat';
UPDATE models SET context_window = 1000000, max_output_tokens = 384000 WHERE name = 'deepseek-reasoner';
UPDATE models SET context_window = 1000000, max_output_tokens = 384000 WHERE name LIKE 'deepseek-v4-%';
