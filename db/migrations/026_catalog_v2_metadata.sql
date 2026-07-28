-- 026_catalog_v2_metadata.sql
-- Versioned MuhiyaCode catalog metadata lives separately from the legacy
-- OpenAI-compatible models table. No routing/candidate fields are present.

CREATE TABLE IF NOT EXISTS model_catalog_metadata (
    model_id VARCHAR(100) PRIMARY KEY REFERENCES models(id) ON DELETE CASCADE,
    tags TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    provider_family VARCHAR(64) NOT NULL DEFAULT 'openai-compatible',
    adapter_version VARCHAR(32) NOT NULL DEFAULT '1',
    compatibility_epoch INTEGER NOT NULL DEFAULT 1 CHECK (compatibility_epoch > 0),
    cache_contract JSONB NOT NULL DEFAULT '{}'::JSONB,
    supported_parameters TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    pricing_rule_set_id VARCHAR(100) NOT NULL DEFAULT '',
    health VARCHAR(32) NOT NULL DEFAULT 'unknown'
        CHECK (health IN ('healthy', 'degraded', 'unavailable', 'unknown')),
    deprecated_at TIMESTAMP WITH TIME ZONE,
    deprecation_message TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO model_catalog_metadata (
    model_id, tags, provider_family, adapter_version, compatibility_epoch,
    cache_contract, supported_parameters, pricing_rule_set_id, health
)
SELECT
    id,
    CASE WHEN muhiyacode_visible THEN ARRAY['muhiyacode']::TEXT[] ELSE ARRAY[]::TEXT[] END,
    CASE
        WHEN lower(name) LIKE 'minimax%' THEN 'minimax-openrouter'
        WHEN lower(name) LIKE 'deepseek%' THEN 'deepseek'
        ELSE 'openai-compatible'
    END,
    '1',
    1,
    CASE
        WHEN lower(name) LIKE 'minimax%' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":true,"supports_prompt_cache":true}'::JSONB
        WHEN lower(name) LIKE 'deepseek%' THEN
            '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":true}'::JSONB
        ELSE
            '{"prefix_order":"system-tools-history-tail","route_scoped":false,"supports_prompt_cache":false}'::JSONB
    END,
    ARRAY['model','messages','stream','tools','tool_choice','max_tokens','temperature','top_p']::TEXT[],
    'pricing:' || id,
    CASE WHEN status = 'active' THEN 'healthy' ELSE 'unavailable' END
FROM models
WHERE name <> 'muhiya-ai-router'
ON CONFLICT (model_id) DO UPDATE SET
    tags = EXCLUDED.tags,
    provider_family = EXCLUDED.provider_family,
    cache_contract = EXCLUDED.cache_contract,
    health = EXCLUDED.health,
    updated_at = CURRENT_TIMESTAMP;

CREATE OR REPLACE FUNCTION ensure_model_catalog_metadata()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.name = 'muhiya-ai-router' THEN
        RETURN NEW;
    END IF;
    INSERT INTO model_catalog_metadata (
        model_id, tags, provider_family, pricing_rule_set_id, health
    ) VALUES (
        NEW.id,
        CASE WHEN NEW.muhiyacode_visible THEN ARRAY['muhiyacode']::TEXT[] ELSE ARRAY[]::TEXT[] END,
        'openai-compatible',
        'pricing:' || NEW.id,
        CASE WHEN NEW.status = 'active' THEN 'healthy' ELSE 'unavailable' END
    )
    ON CONFLICT (model_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS models_catalog_metadata_insert ON models;
CREATE TRIGGER models_catalog_metadata_insert
AFTER INSERT ON models
FOR EACH ROW EXECUTE FUNCTION ensure_model_catalog_metadata();
