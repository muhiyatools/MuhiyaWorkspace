-- probe_model_save.sql
--
-- Reproduces, against the real database, every statement the admin "Save Model
-- Changes" path executes — and reports which one fails, with its SQLSTATE and
-- message. No deploy required, and nothing is written: the whole probe runs
-- inside a transaction that is rolled back at the end.
--
-- Each step is wrapped in its own exception handler, so one failure does not
-- abort the rest (which is what produces the useless "current transaction is
-- aborted" cascade). Every step reports OK or FAILED independently.
--
-- HOW TO RUN in pgAdmin:
--   1. Make sure Auto commit is ON (gear icon in the Query Tool toolbar).
--   2. Edit v_model_name below to the model you were editing when it failed.
--   3. Run the whole script.
--   4. Read the *Messages* tab, not the Data Output tab.

BEGIN;

DO $probe$
DECLARE
    v_model_name  TEXT := 'gpt-5.6-terra';   -- <== change to the failing model
    v_id          TEXT;
    v_meta        RECORD;
BEGIN
    SELECT id INTO v_id FROM models WHERE name = v_model_name;
    IF v_id IS NULL THEN
        RAISE NOTICE 'NO SUCH MODEL: %  (edit v_model_name at the top)', v_model_name;
        RETURN;
    END IF;
    RAISE NOTICE 'probing model % (id=%)', v_model_name, v_id;

    -- Step 1: the models UPDATE, including the column added by migration 032.
    BEGIN
        UPDATE models
           SET prompt_accounting = COALESCE(prompt_accounting, 'inclusive'),
               muhiyacode_visible = muhiyacode_visible,
               status = status
         WHERE id = v_id;
        RAISE NOTICE 'step 1  UPDATE models .............. OK';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 1  UPDATE models .............. FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    -- Step 2: the catalog-metadata upsert, replayed with the row's own values
    -- so only a constraint or type problem can fail it.
    BEGIN
        SELECT * INTO v_meta FROM model_catalog_metadata WHERE model_id = v_id;
        IF FOUND THEN
            INSERT INTO model_catalog_metadata (
                model_id, tags, provider_family, adapter_version, compatibility_epoch,
                cache_contract, supported_parameters, pricing_rule_set_id, health,
                deprecated_at, deprecation_message, updated_at
            ) VALUES (
                v_meta.model_id, v_meta.tags, v_meta.provider_family, v_meta.adapter_version,
                v_meta.compatibility_epoch, v_meta.cache_contract, v_meta.supported_parameters,
                v_meta.pricing_rule_set_id, v_meta.health, v_meta.deprecated_at,
                v_meta.deprecation_message, now()
            )
            ON CONFLICT (model_id) DO UPDATE SET updated_at = now();
            RAISE NOTICE 'step 2  upsert catalog metadata .... OK';
        ELSE
            RAISE NOTICE 'step 2  upsert catalog metadata .... SKIPPED (no metadata row)';
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 2  upsert catalog metadata .... FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    -- Step 3: pricing tiers are deleted and re-inserted on every save.
    BEGIN
        DELETE FROM model_pricing_tiers WHERE model_id = v_id;
        RAISE NOTICE 'step 3  DELETE pricing tiers ....... OK';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 3  DELETE pricing tiers ....... FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    -- Step 4: cache-TTL rates (migration 033).
    BEGIN
        DELETE FROM model_cache_ttl_rates WHERE model_id = v_id;
        RAISE NOTICE 'step 4  DELETE cache ttl rates ..... OK';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 4  DELETE cache ttl rates ..... FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    -- Step 5: price windows (migration 034).
    BEGIN
        DELETE FROM model_price_windows WHERE model_id = v_id;
        RAISE NOTICE 'step 5  DELETE price windows ....... OK';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 5  DELETE price windows ....... FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    -- Step 6: re-insert one representative pricing tier, which is where a
    -- CHECK or UNIQUE constraint would bite.
    BEGIN
        INSERT INTO model_pricing_tiers (
            id, model_id, min_input_tokens_exclusive,
            input_nano_usd_per_million, output_nano_usd_per_million,
            cache_read_nano_usd_per_million, cache_write_nano_usd_per_million, enabled
        ) VALUES (
            'probe-tier-' || v_id, v_id, 272000,
            1000000000, 6000000000, 100000000, 2500000000, TRUE
        );
        RAISE NOTICE 'step 6  INSERT pricing tier ........ OK';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'step 6  INSERT pricing tier ........ FAILED  [%] %', SQLSTATE, SQLERRM;
    END;

    RAISE NOTICE 'probe complete — nothing was written (the transaction rolls back)';
END
$probe$;

ROLLBACK;
