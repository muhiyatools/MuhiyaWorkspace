-- 025_model_pricing_tiers.sql
-- Data-driven exact pricing tiers. Base rates remain on models; these rows
-- override all rates when input_tokens is above the configured threshold.

CREATE TABLE IF NOT EXISTS model_pricing_tiers (
    id VARCHAR(100) PRIMARY KEY,
    model_id VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    min_input_tokens_exclusive BIGINT NOT NULL,
    input_nano_usd_per_million BIGINT NOT NULL,
    output_nano_usd_per_million BIGINT NOT NULL,
    cache_read_nano_usd_per_million BIGINT NOT NULL,
    cache_write_nano_usd_per_million BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(model_id, min_input_tokens_exclusive),
    CHECK (min_input_tokens_exclusive >= 0),
    CHECK (
        input_nano_usd_per_million >= 0 AND
        output_nano_usd_per_million >= 0 AND
        cache_read_nano_usd_per_million >= 0 AND
        cache_write_nano_usd_per_million >= 0
    )
);

INSERT INTO model_pricing_tiers (
    id, model_id, min_input_tokens_exclusive,
    input_nano_usd_per_million, output_nano_usd_per_million,
    cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
)
SELECT
    'pricing-tier-' || id || '-512k', id, 512000,
    600000000, 2400000000, 120000000, 600000000
FROM models
WHERE lower(name) = 'minimax-m3'
ON CONFLICT (model_id, min_input_tokens_exclusive) DO NOTHING;
