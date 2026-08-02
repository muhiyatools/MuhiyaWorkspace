-- 037_model_spec_scores.sql
--
-- Operator-supplied capability ranks, surfaced in the MuhiyaCode model picker as
-- Intelligence / Speed bars alongside a Cost bar derived from the pricing that
-- already exists on this table.
--
-- coding_tier was already read by the MuhiyaCode catalog client
-- (internal/gateway/catalog_discovery.go) and declared in its contract as an
-- "operator/provider supplied capability rank from 1 to 5" — but the gateway
-- never emitted it, so the field was dead end to end. This adds the column that
-- backs it rather than inventing a second name for the same idea.
--
-- Both are 1..5, where 0 means UNKNOWN. Unknown is meaningful: the picker draws
-- a dash instead of an empty bar, because an empty bar reads as "scores zero"
-- and would libel an unrated model.

ALTER TABLE models ADD COLUMN IF NOT EXISTS coding_tier INTEGER NOT NULL DEFAULT 0;
ALTER TABLE models ADD COLUMN IF NOT EXISTS speed_score INTEGER NOT NULL DEFAULT 0;

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_coding_tier_range;
ALTER TABLE models ADD CONSTRAINT models_coding_tier_range
    CHECK (coding_tier BETWEEN 0 AND 5);

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_speed_score_range;
ALTER TABLE models ADD CONSTRAINT models_speed_score_range
    CHECK (speed_score BETWEEN 0 AND 5);

-- Starting ranks for the models this deployment actually serves. These are
-- editable in the admin panel; they are a starting point, not a judgement the
-- gateway enforces anywhere. Only rows still at 0 are touched, so an operator
-- who has already rated a model keeps their value.
UPDATE models SET coding_tier = 5, speed_score = 2
 WHERE target_model = 'deepseek-v4-pro' AND coding_tier = 0;

UPDATE models SET coding_tier = 4, speed_score = 4
 WHERE target_model = 'deepseek-v4-flash' AND coding_tier = 0;

UPDATE models SET coding_tier = 4, speed_score = 3
 WHERE name LIKE 'minimax%' AND coding_tier = 0;
