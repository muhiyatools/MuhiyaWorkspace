-- configure_openrouter_pricing.sql
--
-- Configures gpt-5.6-terra, gpt-5.6-luna and qwen3.7-flash to match the
-- pricing OpenRouter publishes for them, including the context-length tiers
-- and the 5-minute cache rates that the flat model columns cannot express.
--
-- Re-runnable: sections 2 and 3 delete and re-insert the model's tier and TTL
-- rows, so running this twice leaves the same state as running it once.
--
-- Source (captured 2026-08-01):
--
--   gpt-5.6-terra   in $1.00   out $6.00   cached-in $0.10
--                   cache write  from $1.25   (<=272K $1.25,  >272K $2.50)
--   gpt-5.6-luna    in $0.10   out $0.60   cached-in $0.01
--                   cache write  from $0.125  (<=272K $0.125, >272K $0.25)
--   qwen3.7-flash   in $0.03   out $0.13   cached-in $0.006
--                   cache create 5m  <=32K $0.038, <=256K $0.125, >256K $0.25
--                   cache read   5m  <=32K $0.003, <=256K $0.01,  >256K $0.02
--
-- Threshold interpretation: "272K" is read as 272,000 tokens (and 32K/256K as
-- 32,000/256,000), i.e. decimal thousands rather than binary KiB. If OpenRouter
-- means 278,528 / 32,768 / 262,144 these numbers need adjusting; the difference
-- only matters for prompts within ~2% of a boundary.
--
--   psql "$DATABASE_URL" -f db/queries/configure_openrouter_pricing.sql

BEGIN;

\echo '== Before: current stored rates =='
SELECT name,
       input_cost_per_million        AS input,
       output_cost_per_million       AS output,
       cache_read_cost_per_million   AS cache_read,
       cache_write_cost_per_million  AS cache_write
  FROM models
 WHERE name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash')
 ORDER BY name;

-- ===========================================================================
-- SECTION 1 - Base rates.
--
-- READ THIS BEFORE RUNNING. Three of these are corrections, not restatements:
--
--   gpt-5.6-terra cache write   $0.125 -> $1.25   (was 10x BELOW OpenRouter)
--   gpt-5.6-luna  input         $2.00  -> $0.10   (was 20x ABOVE OpenRouter)
--   gpt-5.6-luna  output        $6.00  -> $0.60   (was 10x ABOVE OpenRouter)
--   gpt-5.6-luna  cache read    $0.30  -> $0.01   (was 30x ABOVE OpenRouter)
--   qwen3.7-flash cache read    $0.0063 -> $0.006 (was ~5% above)
--
-- If the Luna figures are a deliberate markup rather than a mistake, delete
-- this section and run only sections 2 and 3 - the tiers below are expressed
-- as absolute rates and would then also need scaling to keep that margin.
-- ===========================================================================

UPDATE models SET
    input_cost_per_million       = 1.00,
    output_cost_per_million      = 6.00,
    cache_read_cost_per_million  = 0.10,
    cache_write_cost_per_million = 1.25,
    input_cost_nano_usd_per_million       = 1000000000,
    output_cost_nano_usd_per_million      = 6000000000,
    cache_read_cost_nano_usd_per_million  = 100000000,
    cache_write_cost_nano_usd_per_million = 1250000000
 WHERE name = 'gpt-5.6-terra';

UPDATE models SET
    input_cost_per_million       = 0.10,
    output_cost_per_million      = 0.60,
    cache_read_cost_per_million  = 0.01,
    cache_write_cost_per_million = 0.125,
    input_cost_nano_usd_per_million       = 100000000,
    output_cost_nano_usd_per_million      = 600000000,
    cache_read_cost_nano_usd_per_million  = 10000000,
    cache_write_cost_nano_usd_per_million = 125000000
 WHERE name = 'gpt-5.6-luna';

-- Qwen publishes no generic "Cache Write" price, only a 5-minute creation
-- price. The base cache-write rate is set to that 5-minute <=32K figure so an
-- upstream that reports a flat cache_creation total (no TTL breakdown) is
-- still billed correctly rather than falling back to the input rate.
UPDATE models SET
    input_cost_per_million       = 0.03,
    output_cost_per_million      = 0.13,
    cache_read_cost_per_million  = 0.006,
    cache_write_cost_per_million = 0.038,
    input_cost_nano_usd_per_million       = 30000000,
    output_cost_nano_usd_per_million      = 130000000,
    cache_read_cost_nano_usd_per_million  = 6000000,
    cache_write_cost_nano_usd_per_million = 38000000
 WHERE name = 'qwen3.7-flash';

-- All three are OpenRouter/OpenAI-dialect upstreams: their prompt token count
-- already includes cached tokens.
UPDATE models SET prompt_accounting = 'inclusive'
 WHERE name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash');

-- ===========================================================================
-- SECTION 2 - Context-length tiers.
--
-- A tier row replaces ALL FOUR base rates above its threshold, so the rates
-- that do not change with context length are restated in each tier.
-- min_input_tokens_exclusive is exclusive: the tier applies when the total
-- prompt is STRICTLY GREATER than the threshold, which is what ">272K" means.
-- ===========================================================================

DELETE FROM model_pricing_tiers
 WHERE model_id IN (SELECT id FROM models
                     WHERE name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash'));

-- gpt-5.6-terra: only the cache-write price moves above 272K.
INSERT INTO model_pricing_tiers (
    id, model_id, min_input_tokens_exclusive,
    input_nano_usd_per_million, output_nano_usd_per_million,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'pricing-tier-' || id || '-272k', id, 272000,
       1000000000, 6000000000, 100000000, 2500000000
  FROM models WHERE name = 'gpt-5.6-terra';

-- gpt-5.6-luna: same shape, one tenth the price.
INSERT INTO model_pricing_tiers (
    id, model_id, min_input_tokens_exclusive,
    input_nano_usd_per_million, output_nano_usd_per_million,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'pricing-tier-' || id || '-272k', id, 272000,
       100000000, 600000000, 10000000, 250000000
  FROM models WHERE name = 'gpt-5.6-luna';

-- qwen3.7-flash: two thresholds. Input/output/cached-in are flat; only the
-- cache prices move, so those three are repeated unchanged in both tiers.
INSERT INTO model_pricing_tiers (
    id, model_id, min_input_tokens_exclusive,
    input_nano_usd_per_million, output_nano_usd_per_million,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'pricing-tier-' || id || '-32k', id, 32000,
       30000000, 130000000, 6000000, 125000000
  FROM models WHERE name = 'qwen3.7-flash';

INSERT INTO model_pricing_tiers (
    id, model_id, min_input_tokens_exclusive,
    input_nano_usd_per_million, output_nano_usd_per_million,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'pricing-tier-' || id || '-256k', id, 256000,
       30000000, 130000000, 6000000, 250000000
  FROM models WHERE name = 'qwen3.7-flash';

-- ===========================================================================
-- SECTION 3 - Five-minute cache rates (qwen3.7-flash only).
--
-- Terra and Luna publish no separate 5-minute rates, so they get no rows here
-- and their 5m classes inherit the tier rates from section 2 - which is the
-- correct behaviour, not an omission.
--
-- A NULL threshold pairs the rate with the model's base rates; a value pairs
-- it with the tier at that same threshold.
-- ===========================================================================

DELETE FROM model_cache_ttl_rates
 WHERE model_id IN (SELECT id FROM models
                     WHERE name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash'));

-- <=32K band (base rates): read $0.003, create $0.038
INSERT INTO model_cache_ttl_rates (
    id, model_id, min_input_tokens_exclusive, ttl,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'cache-ttl-' || id || '-5m-base', id, NULL, '5m', 3000000, 38000000
  FROM models WHERE name = 'qwen3.7-flash';

-- 32K..256K band: read $0.01, create $0.125
INSERT INTO model_cache_ttl_rates (
    id, model_id, min_input_tokens_exclusive, ttl,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'cache-ttl-' || id || '-5m-32k', id, 32000, '5m', 10000000, 125000000
  FROM models WHERE name = 'qwen3.7-flash';

-- >256K band: read $0.02, create $0.25
INSERT INTO model_cache_ttl_rates (
    id, model_id, min_input_tokens_exclusive, ttl,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT 'cache-ttl-' || id || '-5m-256k', id, 256000, '5m', 20000000, 250000000
  FROM models WHERE name = 'qwen3.7-flash';

\echo ''
\echo '== After: base rates =='
SELECT name,
       input_cost_per_million       AS input,
       output_cost_per_million      AS output,
       cache_read_cost_per_million  AS cache_read,
       cache_write_cost_per_million AS cache_write,
       prompt_accounting
  FROM models
 WHERE name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash')
 ORDER BY name;

\echo ''
\echo '== After: context tiers =='
SELECT m.name, t.min_input_tokens_exclusive AS above_tokens,
       t.input_nano_usd_per_million / 1e9        AS input,
       t.output_nano_usd_per_million / 1e9       AS output,
       t.cache_read_nano_usd_per_million / 1e9   AS cache_read,
       t.cache_write_nano_usd_per_million / 1e9  AS cache_write
  FROM model_pricing_tiers t JOIN models m ON m.id = t.model_id
 WHERE m.name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash')
 ORDER BY m.name, t.min_input_tokens_exclusive;

\echo ''
\echo '== After: 5-minute cache rates =='
SELECT m.name,
       COALESCE(r.min_input_tokens_exclusive::text, '(base)') AS above_tokens,
       r.ttl,
       r.cache_read_nano_usd_per_million / 1e9  AS cache_read_5m,
       r.cache_write_nano_usd_per_million / 1e9 AS cache_write_5m
  FROM model_cache_ttl_rates r JOIN models m ON m.id = r.model_id
 WHERE m.name IN ('gpt-5.6-terra', 'gpt-5.6-luna', 'qwen3.7-flash')
 ORDER BY m.name, r.min_input_tokens_exclusive NULLS FIRST;

-- Review the output above, then COMMIT. Roll back if anything looks wrong.
COMMIT;
