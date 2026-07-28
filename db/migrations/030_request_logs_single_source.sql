-- 030: make request_logs the only request-usage and customer-charge authority.
--
-- Existing account_ledger debit rows that do not already have a request log are
-- converted into explicitly marked legacy request rows before the redundant
-- table is removed. This preserves monetary history without allowing the
-- incomplete legacy rows to masquerade as provider token telemetry.

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS session_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS client_request_id VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS attempt_number INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS request_status VARCHAR(32) NOT NULL DEFAULT 'failed',
    ADD COLUMN IF NOT EXISTS streamed BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS cache_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS budget_window_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS budget_window_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS budget_window_reset_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS credits_consumed NUMERIC(24,9)
        GENERATED ALWAYS AS (cost_nano_usd::numeric / 10000000::numeric) STORED;

UPDATE request_logs
   SET request_status = CASE
       WHEN status_code >= 200 AND status_code < 300 THEN 'succeeded'
       WHEN status_code = 402 THEN 'rejected_budget'
       WHEN status_code = 429 THEN 'rejected_rate_limit'
       WHEN status_code = 499 THEN 'cancelled'
       WHEN status_code = 504 THEN 'timed_out'
       ELSE 'failed'
   END
 WHERE request_status = 'failed';

DO $migration$
BEGIN
    IF to_regclass('public.account_ledger') IS NOT NULL THEN
        EXECUTE $sql$
            INSERT INTO request_logs (
                id, virtual_key_id, user_id, model_id, provider_id,
                request_path, status_code, input_tokens, output_tokens,
                cache_read_tokens, cache_write_tokens, cache_miss_tokens,
                cost, cost_nano_usd, latency_ms, error_message, created_at,
                client_app, requested_model, complexity, failover_attempts,
                thinking_level, usage_estimated, upstream_provider,
                request_status, streamed
            )
            SELECT
                COALESCE(NULLIF(ledger.request_id, ''), 'legacy-ledger-' || ledger.id),
                NULL,
                ledger.user_id,
                NULL,
                NULL,
                '/legacy/account-ledger',
                200,
                0,
                0,
                0,
                0,
                NULL,
                ledger.amount_nano_usd::double precision / 1000000000.0,
                ledger.amount_nano_usd,
                0,
                'Recovered from the retired account ledger; token/provider telemetry was unavailable.',
                ledger.created_at,
                'legacy-account-ledger',
                '',
                'legacy-recovery',
                0,
                '',
                TRUE,
                NULL,
                'succeeded',
                FALSE
            FROM account_ledger AS ledger
            WHERE ledger.kind = 'debit'
              AND ledger.amount_nano_usd > 0
            ON CONFLICT (id) DO NOTHING
        $sql$;
        DROP TABLE account_ledger;
    END IF;
END
$migration$;

ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_attempt_positive,
    ADD CONSTRAINT request_logs_attempt_positive CHECK (attempt_number > 0),
    DROP CONSTRAINT IF EXISTS request_logs_cache_epoch_nonnegative,
    ADD CONSTRAINT request_logs_cache_epoch_nonnegative CHECK (cache_epoch >= 0),
    DROP CONSTRAINT IF EXISTS request_logs_tokens_nonnegative,
    ADD CONSTRAINT request_logs_tokens_nonnegative CHECK (
        input_tokens >= 0 AND output_tokens >= 0 AND
        cache_read_tokens >= 0 AND cache_write_tokens >= 0 AND
        (cache_miss_tokens IS NULL OR cache_miss_tokens >= 0)
    ) NOT VALID,
    DROP CONSTRAINT IF EXISTS request_logs_status_valid,
    ADD CONSTRAINT request_logs_status_valid CHECK (
        request_status IN (
            'succeeded', 'failed', 'cancelled', 'timed_out',
            'rejected_budget', 'rejected_rate_limit'
        )
    ),
    DROP CONSTRAINT IF EXISTS request_logs_user_identity_present,
    ADD CONSTRAINT request_logs_user_identity_present CHECK (user_id IS NOT NULL) NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_client_attempt_unique
    ON request_logs (virtual_key_id, client_request_id, attempt_number)
    WHERE virtual_key_id IS NOT NULL AND client_request_id <> '';

CREATE INDEX IF NOT EXISTS idx_request_logs_user_session_created
    ON request_logs (user_id, session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_request_logs_budget_window_created
    ON request_logs (budget_window_id, created_at DESC)
    WHERE budget_window_id IS NOT NULL;

ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_budget_window_id_fkey,
    ADD CONSTRAINT request_logs_budget_window_id_fkey
        FOREIGN KEY (budget_window_id) REFERENCES budget_windows(id) ON DELETE SET NULL;
