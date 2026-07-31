-- undercharge_audit.sql
--
-- Sizes the historical undercharge that migration 032 stops going forward.
--
-- Before 032 the pricing engine subtracted cached tokens from every provider's
-- reported prompt total. That is correct for OpenAI-family upstreams, which
-- report prompt_tokens INCLUDING cached tokens, but wrong for Anthropic, which
-- reports input_tokens EXCLUDING them. Anthropic-family requests therefore had
-- fresh input tokens subtracted away and clamped to zero, and were billed
-- nothing for them.
--
-- These are read-only diagnostics. Retro-billing is a business decision, and
-- nothing here changes a stored charge.
--
-- Run against the production database with:
--   psql "$DATABASE_URL" -f db/queries/undercharge_audit.sql

\echo '== 1. Which models were affected (exclusive accounting) =='
SELECT m.id, m.name, m.prompt_accounting
  FROM models AS m
 WHERE m.prompt_accounting = 'exclusive'
 ORDER BY m.name;

\echo ''
\echo '== 2. Requests at risk, by month =='
-- A request was undercharged only where cached tokens actually existed: with
-- no cache activity the subtraction was a no-op and the charge was correct.
SELECT date_trunc('month', rl.created_at) AS month,
       count(*)                            AS requests,
       sum(rl.input_tokens)                AS reported_input_tokens,
       sum(rl.cache_read_tokens + rl.cache_write_tokens) AS cached_tokens,
       round(sum(rl.cost_nano_usd) / 1e9, 6) AS charged_usd
  FROM request_logs AS rl
  JOIN models AS m ON m.id = rl.model_id
 WHERE m.prompt_accounting = 'exclusive'
   AND rl.request_status = 'succeeded'
   AND (rl.cache_read_tokens + rl.cache_write_tokens) > 0
 GROUP BY 1
 ORDER BY 1;

\echo ''
\echo '== 3. Estimated shortfall, by model =='
-- The old engine billed max(0, input - cached) at the input rate. The correct
-- amount is the full reported input, because for these providers input_tokens
-- never included the cached tokens in the first place. The gap is therefore
-- the difference between those two, priced at the model's CURRENT input rate.
--
-- Caveat: this uses today's price, not the price in force at the time. Rows
-- written before migration 035 carry no rate snapshot, so an exact historical
-- figure is not recoverable — this is a magnitude estimate, not an invoice.
SELECT m.name,
       count(*) AS requests,
       sum(rl.input_tokens) AS unbilled_input_tokens,
       round(
           sum(
               ceil(
                   rl.input_tokens::numeric
                   * m.input_cost_nano_usd_per_million::numeric
                   / 1000000::numeric
               )
           ) / 1e9,
           6
       ) AS estimated_shortfall_usd
  FROM request_logs AS rl
  JOIN models AS m ON m.id = rl.model_id
 WHERE m.prompt_accounting = 'exclusive'
   AND rl.request_status = 'succeeded'
   AND rl.input_tokens > 0
   -- Only rows where the subtraction actually zeroed the input out. Where
   -- cached tokens were fewer than the reported input, part of it was billed.
   AND (rl.cache_read_tokens + rl.cache_write_tokens) >= rl.input_tokens
 GROUP BY m.name
 ORDER BY estimated_shortfall_usd DESC;

\echo ''
\echo '== 4. Upstreams reporting inconsistent usage (post-032 telemetry) =='
-- Populated only for requests served after migration 032. A non-empty result
-- means a provider is contradicting itself, not that we are mispricing.
SELECT usage_anomaly, count(*) AS requests, max(created_at) AS most_recent
  FROM request_logs
 WHERE usage_anomaly <> ''
 GROUP BY usage_anomaly
 ORDER BY requests DESC;
