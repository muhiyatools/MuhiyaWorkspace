-- Performance indexes for the request-logging hot paths.
-- Before this, every proxied request triggered full-table scans on request_logs
-- for spending/budget aggregates, and FK columns were unindexed. All are
-- additive and safe to run on an existing database.

-- Spending/budget windows filter by user_id + created_at (and status_code).
CREATE INDEX IF NOT EXISTS idx_request_logs_user_created
    ON request_logs (user_id, created_at);

-- Per-key usage lookups and rate accounting.
CREATE INDEX IF NOT EXISTS idx_request_logs_virtual_key
    ON request_logs (virtual_key_id);

-- Dashboard/admin log filtering by model and provider.
CREATE INDEX IF NOT EXISTS idx_request_logs_model
    ON request_logs (model_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_provider
    ON request_logs (provider_id);

-- Recent-logs ordering (admin log stream orders by created_at DESC).
CREATE INDEX IF NOT EXISTS idx_request_logs_created
    ON request_logs (created_at DESC);

-- Router/model listing joins models to their provider.
CREATE INDEX IF NOT EXISTS idx_models_provider
    ON models (provider_id);

-- Model resolution filters on status and matches name/display_name.
CREATE INDEX IF NOT EXISTS idx_models_status
    ON models (status);
