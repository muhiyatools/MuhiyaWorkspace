-- 018_model_supports_thinking.sql: an explicit per-model reasoning/thinking
-- capability flag, mirroring supports_vision (016).
--
-- Thinking routing previously relied on a NAME heuristic in the proxy
-- (target_model containing reasoner/r1/o1/o3/claude-3-7). That silently
-- misclassifies modern reasoning models named otherwise, and steers the
-- thinking-On router tier by a substring guess. This operator-set flag becomes
-- the source of truth; the name heuristic remains only as a fallback for
-- unflagged rows.

ALTER TABLE models ADD COLUMN IF NOT EXISTS supports_thinking BOOLEAN NOT NULL DEFAULT false;

-- Backfill the flag from the historical heuristic so existing reasoning models
-- keep their behavior without manual re-flagging. Word-boundary match on r1 so
-- it does not catch unrelated substrings.
UPDATE models
SET supports_thinking = true
WHERE supports_thinking = false
  AND target_model ~* '(reasoner|(^|[^a-z0-9])r1([^a-z0-9]|$)|o1|o3|claude-3-7)';
