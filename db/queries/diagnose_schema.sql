-- diagnose_schema.sql
--
-- Answers "why does saving a model return 500?" in one pass.
--
-- The gateway applies migrations at boot and records each one in _migrations.
-- When the admin footer reports fewer migrations than db/migrations/ contains,
-- the schema is behind the binary: the write path then references columns and
-- tables that do not exist, and the driver error surfaces as an opaque 500.
--
-- Runs in pgAdmin as-is. It is one statement returning one grid — no psql
-- meta-commands (\echo is psql-only and errors in pgAdmin), and no reserved
-- words as aliases ("column" is reserved in Postgres, which is why an earlier
-- version of this file failed with SQLSTATE 42601).
--
-- Read the grid top to bottom. Anything marked MISSING is the cause.

SELECT * FROM (
    -- 1. How far the recorded migration history has got.
    SELECT 1 AS sort_group,
           'migrations'                  AS check_kind,
           'applied_count'               AS item,
           count(*)::text                AS status,
           'expect 35'                   AS supplied_by
      FROM _migrations

    UNION ALL
    SELECT 1, 'migrations', 'latest_applied',
           COALESCE(max(name), '(none)'),
           'expect 035_request_pricing_audit.sql'
      FROM _migrations

    UNION ALL
    -- 2. Tables the model write path needs.
    SELECT 2, 'table', expected.table_name,
           CASE WHEN t.table_name IS NULL THEN 'MISSING' ELSE 'present' END,
           expected.supplied_by
      FROM (VALUES
            ('model_pricing_tiers',    '025_model_pricing_tiers.sql'),
            ('model_catalog_metadata', '026_catalog_v2_metadata.sql'),
            ('model_cache_ttl_rates',  '033_model_cache_ttl_rates.sql'),
            ('model_price_windows',    '034_model_price_windows.sql'),
            ('request_pricing_lines',  '035_request_pricing_audit.sql')
           ) AS expected(table_name, supplied_by)
      LEFT JOIN information_schema.tables AS t
             ON t.table_schema = current_schema()
            AND t.table_name = expected.table_name

    UNION ALL
    -- 3. Columns the model write path needs.
    SELECT 3, 'column', expected.table_name || '.' || expected.column_name,
           CASE WHEN c.column_name IS NULL THEN 'MISSING' ELSE 'present' END,
           expected.supplied_by
      FROM (VALUES
            ('models',       'supports_vision',                '016_model_supports_vision.sql'),
            ('models',       'supports_thinking',              '018_model_supports_thinking.sql'),
            ('models',       'muhiyacode_visible',             '021_model_muhiyacode_visible.sql'),
            ('models',       'input_cost_nano_usd_per_million','023_exact_money_foundation.sql'),
            ('models',       'prompt_accounting',              '032_prompt_accounting.sql'),
            ('request_logs', 'cost_nano_usd',                  '023_exact_money_foundation.sql'),
            ('request_logs', 'usage_anomaly',                  '032_prompt_accounting.sql'),
            ('request_logs', 'pricing_rule_set_id',            '035_request_pricing_audit.sql')
           ) AS expected(table_name, column_name, supplied_by)
      LEFT JOIN information_schema.columns AS c
             ON c.table_schema = current_schema()
            AND c.table_name = expected.table_name
            AND c.column_name = expected.column_name
) AS report
ORDER BY sort_group, status DESC, item;

-- Reading the result:
--
--   Anything MISSING  -> the schema is behind the binary. Restart the gateway
--                        and watch the boot log for "[MIGRATION] Applied: ...".
--                        The current build also refuses to start when a required
--                        object is absent, naming the migration.
--
--   All present, but  -> the schema was applied out of band (setup_database.sql
--   applied_count<35     or the files run by hand) without recording it. Safe to
--                        fix: every migration body is idempotent
--                        (ADD COLUMN IF NOT EXISTS / CREATE TABLE IF NOT EXISTS),
--                        so a restart re-runs and records them without damage.
