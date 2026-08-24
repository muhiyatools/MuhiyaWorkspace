-- 042_model_spec_scores_decimal.sql (renumbered from 038 to resolve a duplicate migration number)
--
-- Widen the model-picker capability ranks from INTEGER to one decimal place.
--
-- Whole numbers made every rated model land on one of five identical bar
-- lengths, so a picker full of capable models drew a column of visually
-- identical meters and the comparison carried no information. A tenth of a rung
-- is enough to separate "4.2" from "4.5" on a ten-cell bar without inviting
-- false precision.
--
-- 0 still means UNRATED. NUMERIC(2,1) holds 0.0 .. 5.0 exactly; a float would
-- make 4.3 unrepresentable and turn an operator's typed value into 4.2999...

ALTER TABLE models
    ALTER COLUMN coding_tier TYPE NUMERIC(2,1) USING coding_tier::NUMERIC(2,1);
ALTER TABLE models
    ALTER COLUMN speed_score TYPE NUMERIC(2,1) USING speed_score::NUMERIC(2,1);

ALTER TABLE models ALTER COLUMN coding_tier SET DEFAULT 0;
ALTER TABLE models ALTER COLUMN speed_score SET DEFAULT 0;

-- The 037 constraints were written against integers; BETWEEN still holds for
-- decimals, but recreate them so the range is stated against the new type.
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_coding_tier_range;
ALTER TABLE models ADD CONSTRAINT models_coding_tier_range
    CHECK (coding_tier >= 0 AND coding_tier <= 5);

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_speed_score_range;
ALTER TABLE models ADD CONSTRAINT models_speed_score_range
    CHECK (speed_score >= 0 AND speed_score <= 5);
