-- 016_model_supports_vision.sql: an explicit per-model vision capability flag.
--
-- Vision routing previously relied on a NAME heuristic (model name containing
-- gemma/gemini/gpt-4o/vision/...), which silently failed for a vision model
-- named otherwise (e.g. qwen3-vl-8b) — the router then found "no active models"
-- for an image request. This operator-set flag is the source of truth; the name
-- heuristic remains only as a fallback for unflagged rows.

ALTER TABLE models ADD COLUMN IF NOT EXISTS supports_vision BOOLEAN NOT NULL DEFAULT false;

-- Flag the seeded free Gemma vision model (idempotent).
UPDATE models SET supports_vision = true WHERE name = 'gemma-4-vision';
