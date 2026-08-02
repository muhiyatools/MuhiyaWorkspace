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
    muhiyacode_visible BOOLEAN NOT NULL DEFAULT FALSE,
    supports_thinking BOOLEAN NOT NULL DEFAULT FALSE,
    -- Whether this provider's reported prompt token count already includes
    -- cached tokens. Anthropic reports them separately ('exclusive'); every
    -- other upstream we speak to folds them in ('inclusive'). Pricing needs
    -- this to avoid subtracting tokens that were never in the total.
    prompt_accounting VARCHAR(16) NOT NULL DEFAULT 'inclusive'
        CHECK (prompt_accounting IN ('inclusive', 'exclusive')),
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
    -- Pricing provenance: enough to re-derive this charge without the models
    -- table, which may since have been re-priced. See request_pricing_lines.
    pricing_rule_set_id VARCHAR(100) NOT NULL DEFAULT '',
    pricing_tier_threshold BIGINT,
    price_window_id VARCHAR(100) NOT NULL DEFAULT '',
    price_multiplier_num BIGINT NOT NULL DEFAULT 1,
    price_multiplier_den BIGINT NOT NULL DEFAULT 1 CHECK (price_multiplier_den > 0),
    priced_at TIMESTAMP WITH TIME ZONE,
    prompt_accounting VARCHAR(16) NOT NULL DEFAULT 'inclusive',
    usage_anomaly VARCHAR(64) NOT NULL DEFAULT '',
    upstream_cost_nano_usd BIGINT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 7a. Per-token-class derivation of every charge. A cost without its
-- breakdown is recorded but not auditable, so these rows are written in the
-- same transaction as the request log they belong to.
CREATE TABLE IF NOT EXISTS request_pricing_lines (
    request_log_id VARCHAR(100) NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
    token_class VARCHAR(32) NOT NULL,
    tokens BIGINT NOT NULL CHECK (tokens >= 0),
    rate_nano_usd_per_million BIGINT NOT NULL CHECK (rate_nano_usd_per_million >= 0),
    cost_nano_usd BIGINT NOT NULL CHECK (cost_nano_usd >= 0),
    PRIMARY KEY (request_log_id, token_class),
    CHECK (token_class IN (
        'input_fresh', 'output',
        'cache_read', 'cache_read_5m',
        'cache_write', 'cache_write_5m', 'cache_write_1h'
    ))
);

-- 7b. Cache rates that vary by requested entry lifetime. A NULL rate inherits
-- from the model (or its context tier), so a model with no rows here prices
-- exactly as it would without this table.
CREATE TABLE IF NOT EXISTS model_cache_ttl_rates (
    id VARCHAR(100) PRIMARY KEY,
    model_id VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    min_input_tokens_exclusive BIGINT,
    ttl VARCHAR(16) NOT NULL CHECK (ttl IN ('5m', '1h', 'default')),
    cache_read_nano_usd_per_million BIGINT,
    cache_write_nano_usd_per_million BIGINT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 7c. Time-of-day price scaling (peak/off-peak). The multiplier is an exact
-- rational so "2x" is exactly 2x. Boundaries are UTC minutes-of-day;
-- start > end wraps midnight. Exactly one window applies to a request.
CREATE TABLE IF NOT EXISTS model_price_windows (
    id VARCHAR(100) PRIMARY KEY,
    model_id VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    label VARCHAR(64) NOT NULL DEFAULT '',
    start_minute_utc INTEGER NOT NULL CHECK (start_minute_utc >= 0 AND start_minute_utc < 1440),
    end_minute_utc INTEGER NOT NULL CHECK (end_minute_utc >= 0 AND end_minute_utc <= 1440),
    weekday_mask INTEGER NOT NULL DEFAULT 127 CHECK (weekday_mask >= 1 AND weekday_mask <= 127),
    multiplier_num BIGINT NOT NULL CHECK (multiplier_num >= 0),
    multiplier_den BIGINT NOT NULL DEFAULT 1 CHECK (multiplier_den > 0),
    applies_to TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from TIMESTAMP WITH TIME ZONE,
    effective_until TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (start_minute_utc <> end_minute_utc)
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
-- model-qwen-flash is marked muhiyacode_visible=false: it is intended for
-- MuhiyaChat only, not the coding agent (explicit operator decision —
-- do not default it to true on a future re-seed).
INSERT INTO models (
    id, name, provider_id, target_model,
    input_cost_per_million, output_cost_per_million,
    cache_read_cost_per_million, cache_write_cost_per_million,
    status, routing_tier, model_type, price_per_minute, transcribe,
    context_window, max_output_tokens, display_name, description, owned_by,
    muhiyacode_visible, supports_thinking
) VALUES
('model-gpt4o', 'gpt-4o', 'openai', 'gpt-4o', 2.50, 10.00, 1.25, 2.50, 'inactive', 'none', 'llm', 0.0, false, 128000, 4096, 'GPT-4o', 'OpenAI flagship model', 'openai', false, false),
('model-claude', 'claude-3-5-sonnet', 'anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 0.30, 3.75, 'inactive', 'none', 'llm', 0.0, false, 200000, 8192, 'Claude 3.5 Sonnet', 'Anthropic high-intelligence model', 'anthropic', false, false),
('model-deepseek', 'deepseek-chat', 'deepseek', 'deepseek-v4-flash', 0.14, 0.28, 0.0028, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek Chat (legacy alias)', 'Legacy alias routed to DeepSeek V4 Flash', 'deepseek', false, true),
('model-deepseek-r1', 'deepseek-reasoner', 'deepseek', 'deepseek-v4-flash', 0.14, 0.28, 0.0028, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek Reasoner (legacy alias)', 'Legacy reasoning alias routed to DeepSeek V4 Flash', 'deepseek', false, true),
('model-deepseek-flash', 'deepseek-v4-flash', 'deepseek', 'deepseek-v4-flash', 0.14, 0.28, 0.0028, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek V4 Flash', 'DeepSeek V4 Flash with thinking and non-thinking modes', 'deepseek', false, true),
('model-deepseek-pro', 'deepseek-v4-pro', 'deepseek', 'deepseek-v4-pro', 0.435, 0.87, 0.003625, 0.00, 'inactive', 'none', 'llm', 0.0, false, 1000000, 384000, 'DeepSeek V4 Pro', 'DeepSeek V4 Pro with thinking and non-thinking modes', 'deepseek', false, true),
('model-qwen-flash', 'qwen3.7-flash', 'qwen', 'qwen-2.5-flash', 0.05, 0.10, 0.02, 0.05, 'active', 'none', 'llm', 0.0, false, 64000, 8192, 'Qwen 3.7 Flash', 'Alibaba Qwen Flash model', 'qwen', false, false),
('model-whisper', 'whisper-1', 'openai', 'whisper-1', 0.00, 0.00, 0.00, 0.00, 'inactive', 'none', 'transcript', 0.006, true, 0, 0, 'Whisper 1', 'OpenAI speech-to-text model', 'openai', false, false)
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
