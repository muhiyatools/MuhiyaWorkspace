-- 010: per-(user, budget window) credit-charge watermark.
--
-- Makes DeductExtraCreditsIfExceeded idempotent and concurrency-safe (finding
-- F1). Before this, the deduction computed each request's over-budget share from
-- the raw spend sum with no memory, so two concurrent 2xx requests for the same
-- user each saw the combined spend and each charged the overage -> double-charge.
--
-- last_billed_spend records the in-period spend level already assessed for the
-- window. The deduction charges only the INCREASE in over-budget spend above this
-- watermark, so re-running with the same (or a lower) spend charges nothing. The
-- watermark falls when a rolling window's period rolls over or a bonus reset
-- (migration 011+) raises the usage floor; it is never refunded.
--
-- Derived state, not an audit trail: a deleted user's rows cascade away. Rows can
-- orphan when a plan's budget_windows are deleted+reinserted (new window ids) on a
-- plan edit; that harmlessly re-initializes the watermark on the next request
-- (which also coincides with plan_assigned_at re-anchoring), and never
-- double-charges.
CREATE TABLE IF NOT EXISTS user_window_charge_state (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    window_id         TEXT NOT NULL,
    last_billed_spend DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, window_id)
);
