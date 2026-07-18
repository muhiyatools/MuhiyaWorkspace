-- 019_default_transcription_language.sql: the server-side default language the
-- gateway injects into a transcription request when the client sends none.
--
-- Some providers behind OpenRouter reject a transcription with no language, and
-- Whisper otherwise auto-detects — often mis-detecting short/accented Arabic as
-- English. Defaulting to Arabic ('ar') matches the product's primary audience;
-- MuhiyaChat's per-user setting overrides it per request. Idempotent.

INSERT INTO system_settings (key, value)
VALUES ('default_transcription_language', 'ar')
ON CONFLICT (key) DO NOTHING;
