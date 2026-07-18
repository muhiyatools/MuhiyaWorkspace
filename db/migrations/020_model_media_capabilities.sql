-- 020: Per-model attachment (media) capability tags.
--
-- Extends the capability system (016 vision, 018 thinking) so EVERY input
-- modality a model can accept is an operator-set flag the router and clients
-- read — never a hardcoded platform list:
--   supports_audio      — accepts audio input parts (input_audio)
--   supports_video      — accepts video input parts (video_url)
--   supports_documents  — accepts document input parts (file, e.g. PDF)
--   max_attachment_mb   — per-file size cap for THIS model (0 = client default)
--   accepted_mime_types — optional comma-separated MIME allowlist that narrows
--                         the flag-derived set (empty = derived from flags)
--
-- All default FALSE/0/'' so existing rows and text-only routing behave exactly
-- as before this migration.
ALTER TABLE models ADD COLUMN IF NOT EXISTS supports_audio BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE models ADD COLUMN IF NOT EXISTS supports_video BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE models ADD COLUMN IF NOT EXISTS supports_documents BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE models ADD COLUMN IF NOT EXISTS max_attachment_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE models ADD COLUMN IF NOT EXISTS accepted_mime_types TEXT NOT NULL DEFAULT '';

-- OpenRouter parses PDFs for ANY chat model via its file-parser plugin (the
-- gateway pins the free `pdf-text` engine when forwarding a file part), so
-- every non-transcription model on the OpenRouter provider genuinely accepts
-- document input out of the box. Flag them so document requests route there
-- immediately instead of dead-ending until an operator hand-flags a row.
UPDATE models
   SET supports_documents = TRUE
 WHERE provider_id = 'openrouter'
   AND COALESCE(transcribe, FALSE) = FALSE;
