-- =========================================================================
-- setup_database.sql: Complete database schema and seed data for MuhiyaLLM
-- Run this script in pgAdmin (Query Tool) to build and seed your database.
-- =========================================================================

-- =========================================================================
-- PART 1: SCHEMA CREATION (IDEMPOTENT)
-- =========================================================================

-- 1. Plans
CREATE TABLE IF NOT EXISTS plans (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    rpm_limit INTEGER NOT NULL DEFAULT 0,
    tpm_limit INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2. Budget Windows
CREATE TABLE IF NOT EXISTS budget_windows (
    id VARCHAR(100) PRIMARY KEY,
    plan_id VARCHAR(100) NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    duration_seconds INTEGER NOT NULL,
    budget_usd DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 3. Users
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    plan_id VARCHAR(100) NOT NULL REFERENCES plans(id),
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    plan_assigned_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 4. Virtual Keys
CREATE TABLE IF NOT EXISTS virtual_keys (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'revoked')),
    expires_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5. Providers
CREATE TABLE IF NOT EXISTS providers (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    api_key TEXT NOT NULL,
    base_url TEXT NOT NULL,
    anthropic_base_url TEXT NOT NULL DEFAULT '',
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'inactive')),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 6. Models
CREATE TABLE IF NOT EXISTS models (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    provider_id VARCHAR(100) NOT NULL REFERENCES providers(id),
    target_model VARCHAR(255) NOT NULL,
    input_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    output_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    cache_read_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    cache_write_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'inactive')),
    routing_tier VARCHAR(50) DEFAULT 'none',
    model_type VARCHAR(50) DEFAULT 'llm',
    price_per_minute DOUBLE PRECISION DEFAULT 0.0,
    transcribe BOOLEAN DEFAULT FALSE,
    context_window INTEGER DEFAULT 0,
    max_output_tokens INTEGER DEFAULT 0,
    display_name VARCHAR(255) DEFAULT '',
    description TEXT DEFAULT '',
    owned_by VARCHAR(100) DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 7. Request Logs
CREATE TABLE IF NOT EXISTS request_logs (
    id VARCHAR(100) PRIMARY KEY,
    virtual_key_id VARCHAR(100) NOT NULL REFERENCES virtual_keys(id) ON DELETE CASCADE,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model_id VARCHAR(100) REFERENCES models(id),
    provider_id VARCHAR(100) REFERENCES providers(id),
    request_path VARCHAR(255) NOT NULL,
    status_code INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    latency_ms INTEGER NOT NULL,
    error_message TEXT,
    client_app VARCHAR(255),
    requested_model VARCHAR(255) DEFAULT '',
    complexity VARCHAR(50) DEFAULT '',
    failover_attempts INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 8. System Settings
CREATE TABLE IF NOT EXISTS system_settings (
    key VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL
);

-- 9. User Topups
CREATE TABLE IF NOT EXISTS user_topups (
    id VARCHAR(100) PRIMARY KEY,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credits DOUBLE PRECISION NOT NULL,
    used_credits DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- =========================================================================
-- PART 2: CONSTRAINTS & CLEANUP (IDEMPOTENT)
-- =========================================================================
ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_model_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_model_id_fkey FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE SET NULL;

ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_provider_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE SET NULL;

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_provider_id_fkey;
ALTER TABLE models ADD CONSTRAINT models_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE;

-- =========================================================================
-- PART 3: BASE DATA SEEDING (ON CONFLICT DO NOTHING / UPDATE)
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
('openai', 'OpenAI', '', 'https://api.openai.com/v1', '', 'inactive'),
('anthropic', 'Anthropic', '', '', 'https://api.anthropic.com', 'inactive'),
('deepseek', 'DeepSeek', '', 'https://api.deepseek.com', 'https://api.deepseek.com/anthropic', 'inactive')
ON CONFLICT (id) DO NOTHING;

-- 5. Base Models
-- muhiyacode_visible is set explicitly (matching status) rather than left to
-- its column default of FALSE: MuhiyaCode's model picker discovers ONLY
-- models with this flag set, regardless of status, so a raw-SQL insert that
-- omits it stays permanently absent from the coding agent's /model selector
-- even once activated via the admin panel's status toggle. The
-- model_catalog_metadata row (cache contract, provider family) is created
-- automatically by the models AFTER INSERT trigger; no companion insert is
-- needed here.
--
-- model-qwen-flash is marked muhiyacode_visible=true to match its own
-- status='active' (the same rule 021's backfill applied to every
-- pre-existing row) — flip it to false in the admin panel if this model is
-- intended for MuhiyaChat only, not the coding agent.
INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by,
    muhiyacode_visible
) VALUES
('model-gpt4o', 'gpt-4o', 'openai', 'gpt-4o', 2.50, 10.00, 1.25, 2.50, 'inactive', 'none', 'llm', 0.0, false, 128000, 4096, 'GPT-4o', 'OpenAI flagship model', 'openai', false),
('model-claude', 'claude-3-5-sonnet', 'anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 0.30, 3.75, 'inactive', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.5 Sonnet', 'Anthropic high-intelligence model', 'anthropic', false),
('model-deepseek', 'deepseek-chat', 'deepseek', 'deepseek-chat', 0.14, 0.28, 0.07, 0.14, 'inactive', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek Chat', 'DeepSeek cheap general-purpose model', 'deepseek', false),
('model-deepseek-r1', 'deepseek-reasoner', 'deepseek', 'deepseek-reasoner', 0.55, 2.19, 0.14, 0.55, 'inactive', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek Reasoner', 'DeepSeek reasoning model (R1)', 'deepseek', false),
('model-deepseek-flash', 'deepseek-v4-flash', 'deepseek', 'deepseek-chat', 0.14, 0.28, 0.07, 0.14, 'inactive', 'none', 'llm', 0.0, false, 64000, 8192, 'DeepSeek v4 Flash', 'DeepSeek flash model', 'deepseek', false),
('model-qwen-flash', 'qwen3.7-flash', 'qwen', 'qwen-2.5-flash', 0.05, 0.10, 0.02, 0.05, 'active', 'none', 'llm', 0.0, false, 64000, 8192, 'Qwen 3.7 Flash', 'Alibaba Qwen Flash model', 'qwen', true),
('model-whisper', 'whisper-1', 'openai', 'whisper-1', 0.00, 0.00, 0.00, 0.00, 'inactive', 'none', 'transcript', 0.006, true, 0, 0, 'Whisper 1', 'OpenAI speech-to-text model', 'openai', false)
ON CONFLICT (id) DO NOTHING;

-- =========================================================================
-- PART 4: DEFAULT USER FOR IMMEDIATE WORKING FUNCTIONALITY
-- =========================================================================

-- 1. Create default active user (owns whatever keys you mint for it below).
INSERT INTO users (id, name, email, plan_id, status) VALUES
('usr-admin', 'Admin User', 'admin@muhiya.local', 'plan-dev', 'active')
ON CONFLICT (id) DO NOTHING;

-- SECURITY: a predictable seeded virtual key used to be inserted here
-- ('key-admin-test-12345'), active and billable to usr-admin from the moment
-- this script ran. Anyone who found or guessed it could consume credits on
-- your account. Virtual keys are now stored as a salted hash (see migration
-- 006), so a key can no longer be minted by hand in SQL anyway - generate one
-- through the admin dashboard or `POST /api/keys` (both use crypto/rand) and
-- copy the plaintext token shown ONCE at creation time; it is never stored or
-- shown again.
--
-- If this script was run before and the legacy key is still active on an
-- existing database, revoke it explicitly:
--   UPDATE virtual_keys SET status = 'revoked' WHERE id = 'key-admin-test-12345';
