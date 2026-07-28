-- Automatic model selection is no longer part of the gateway contract.
-- Keep the legacy column for downgrade/schema compatibility, but normalize
-- every existing row so no stale tier can be interpreted as active routing
-- metadata by older admin clients.

UPDATE models
SET routing_tier = 'none'
WHERE routing_tier IS DISTINCT FROM 'none';

-- Stable keyset ordering support for the paginated Admin audit surfaces.
CREATE INDEX IF NOT EXISTS idx_request_logs_created_id
    ON request_logs (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_budget_reservations_created
    ON budget_reservations (created_at DESC);
