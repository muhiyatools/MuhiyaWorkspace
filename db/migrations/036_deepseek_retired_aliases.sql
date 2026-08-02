-- 036_deepseek_retired_aliases.sql
--
-- DeepSeek retired the `deepseek-chat` and `deepseek-reasoner` model names on
-- 2026-07-24 15:59 UTC. During the deprecation window they aliased the
-- non-thinking and thinking modes of deepseek-v4-flash; after it they are simply
-- not served.
--
-- target_model is the value the gateway sends upstream as the model name
-- (proxy/handler.go: bodyMap["model"] = model.TargetModel), so any row still
-- pointing at a retired alias makes every request to that model fail. seed.sql
-- shipped `deepseek-v4-flash` with target_model = 'deepseek-chat', and no earlier
-- migration repaired it — 007 only corrected the context/output limits.
--
-- Repoint every row that still targets a retired alias at deepseek-v4-flash,
-- which is what the alias resolved to. Rows already pointing at a live V4 name
-- are untouched.

UPDATE models
   SET target_model = 'deepseek-v4-flash'
 WHERE target_model IN ('deepseek-chat', 'deepseek-reasoner');

-- The V4 models both have a reasoning mode. supports_thinking is the gateway's
-- source of truth for emitting the thinking parameter (the model NAME is not a
-- reliable signal, which is why the name heuristic was replaced by this flag),
-- so a NULL/false flag here silently strips thinking configuration.
UPDATE models
   SET supports_thinking = TRUE
 WHERE target_model IN ('deepseek-v4-flash', 'deepseek-v4-pro')
   AND supports_thinking IS DISTINCT FROM TRUE;

-- Re-assert the documented V4 limits for any row that arrived after 007.
UPDATE models
   SET context_window    = 1000000,
       max_output_tokens = 384000
 WHERE target_model IN ('deepseek-v4-flash', 'deepseek-v4-pro')
   AND (context_window <> 1000000 OR max_output_tokens <> 384000);
