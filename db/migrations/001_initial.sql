-- 001_initial.sql: Full schema for MuhiyaLLM Gateway
-- This migration creates all tables with their final column set.
-- All statements are idempotent (IF NOT EXISTS) so it's safe on existing databases.

-- ============================================================
-- 1. Plans
-- ============================================================
CREATE TABLE IF NOT EXISTS plans (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    rpm_limit INTEGER NOT NULL DEFAULT 0,
    tpm_limit INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 2. Budget Windows
-- ============================================================
CREATE TABLE IF NOT EXISTS budget_windows (
    id VARCHAR(100) PRIMARY KEY,
    plan_id VARCHAR(100) NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    duration_seconds INTEGER NOT NULL,
    budget_usd DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 3. Users
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    plan_id VARCHAR(100) NOT NULL REFERENCES plans(id),
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    plan_assigned_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 4. Virtual Keys
-- ============================================================
CREATE TABLE IF NOT EXISTS virtual_keys (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'revoked')),
    expires_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 5. Providers
-- ============================================================
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

-- ============================================================
-- 6. Models (with all evolution columns included)
-- ============================================================
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
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 7. Request Logs (with all evolution columns included)
-- ============================================================
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

-- ============================================================
-- 8. System Settings
-- ============================================================
CREATE TABLE IF NOT EXISTS system_settings (
    key VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL
);

-- ============================================================
-- 9. User Topups
-- ============================================================
CREATE TABLE IF NOT EXISTS user_topups (
    id VARCHAR(100) PRIMARY KEY,
    user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credits DOUBLE PRECISION NOT NULL,
    used_credits DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- 10. Backfill: Add missing columns on existing tables (idempotent)
-- ============================================================
ALTER TABLE models ADD COLUMN IF NOT EXISTS routing_tier VARCHAR(50) DEFAULT 'none';
ALTER TABLE models ADD COLUMN IF NOT EXISTS model_type VARCHAR(50) DEFAULT 'llm';
ALTER TABLE models ADD COLUMN IF NOT EXISTS price_per_minute DOUBLE PRECISION DEFAULT 0.0;
ALTER TABLE models ADD COLUMN IF NOT EXISTS transcribe BOOLEAN DEFAULT FALSE;

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS requested_model VARCHAR(255) DEFAULT '';
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS complexity VARCHAR(50) DEFAULT '';
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS failover_attempts INTEGER DEFAULT 0;

-- ============================================================
-- 11. Fix foreign key constraints for correct ON DELETE behavior
-- ============================================================
ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_model_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_model_id_fkey FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE SET NULL;

ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_provider_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE SET NULL;

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_provider_id_fkey;
ALTER TABLE models ADD CONSTRAINT models_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE;

-- ============================================================
-- 12. Set default values for any NULL rows (one-time cleanup)
-- ============================================================
UPDATE models SET routing_tier = 'none' WHERE routing_tier IS NULL;
UPDATE models SET model_type = 'llm' WHERE model_type IS NULL;
UPDATE models SET price_per_minute = 0.0 WHERE price_per_minute IS NULL;
UPDATE models SET transcribe = FALSE WHERE transcribe IS NULL;
