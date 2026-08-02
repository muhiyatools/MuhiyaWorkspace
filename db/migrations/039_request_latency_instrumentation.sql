-- 039_request_latency_instrumentation.sql: make a slow turn explainable.
--
-- Two numbers were missing from request_logs, and without them "why did that
-- request take five minutes?" could only be answered by guessing.
--
-- reasoning_tokens
--   On a thinking model, reasoning tokens are counted INSIDE completion_tokens
--   (DeepSeek reports the split under completion_tokens_details). A turn that
--   billed 27,255 output tokens might have written 27,000 visible tokens, or it
--   might have reasoned for four minutes and written three lines — identical in
--   every figure we stored. This separates them.
--
-- first_token_ms
--   Time from dispatching the upstream request to forwarding its first SSE data
--   frame. Splits provider queue + prompt prefill (which prompt caching can fix)
--   from generation (which only fewer tokens can fix). Previously both were
--   collapsed into latency_ms, so cache work could not be attributed.
--
-- Both are nullable on purpose: 0 is a legitimate value for each, and older rows
-- must stay distinguishable from a genuine zero.

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS reasoning_tokens INTEGER;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS first_token_ms INTEGER;

-- Slow-turn forensics are always scoped to a client and a window.
CREATE INDEX IF NOT EXISTS idx_request_logs_client_created
    ON request_logs (client_app, created_at DESC);
