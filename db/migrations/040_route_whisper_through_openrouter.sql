-- 040_route_whisper_through_openrouter.sql: align the fixed MuhiyaCode
-- transcription contract with the provider used by the hosted gateway.
--
-- The client-facing virtual model remains exactly "whisper-1". OpenRouter's
-- upstream slug is "openai/whisper-1". Do not activate the model here: the
-- operator must still provide an OpenRouter key and explicitly enable the
-- provider/model so a migration cannot create unexpected paid traffic.

UPDATE models
SET provider_id = 'openrouter',
    target_model = 'openai/whisper-1',
    description = 'OpenAI speech-to-text model via OpenRouter',
    owned_by = 'openai'
WHERE name = 'whisper-1'
  AND EXISTS (
    SELECT 1 FROM providers WHERE id = 'openrouter'
  );
