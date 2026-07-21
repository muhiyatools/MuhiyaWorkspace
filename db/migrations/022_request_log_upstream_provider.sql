-- Record WHICH upstream provider actually served each request.
--
-- provider_id already on this table is our own catalog row ("openrouter",
-- "deepseek") — the gateway config that was used. It is set before the request
-- is even sent, so it can never answer the question that matters for caching:
-- OpenRouter load-balances one model slug across many upstreams (minimax-m3 is
-- served by nine), and each upstream keeps its OWN prompt cache. A request that
-- lands on a different upstream than the previous one re-reads the entire
-- conversation at full input price, with byte-identical input and no trace
-- anywhere.
--
-- That failure was diagnosed by hand from timing and cache-read columns. This
-- column makes it visible directly: correlate a low cache_read_tokens against a
-- change in upstream_provider and the cause is on the screen.
--
-- Nullable by design: rows predating this column, and every direct (non-routed)
-- provider connection, legitimately have no value.

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS upstream_provider VARCHAR(100);

-- The diagnostic query this exists for is "show me this key's recent requests
-- with their upstream", so the index matches that access path.
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream
    ON request_logs (virtual_key_id, created_at DESC)
    WHERE upstream_provider IS NOT NULL;
