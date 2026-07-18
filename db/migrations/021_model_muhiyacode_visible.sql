-- 021_model_muhiyacode_visible.sql: per-model discoverability for the MuhiyaCode app.
--
-- MuhiyaCode should only offer models an operator has explicitly published to it.
-- Some models exist purely for MuhiyaChat (e.g. Gemini 2.5 Flash Lite for image
-- input) and must not appear in the coding agent's model picker. This flag gates
-- the /v1/models listing ONLY when the caller is the MuhiyaCode app
-- (X-Client-App: MuhiyaCode) — every other client, exact-name inference, and the
-- router are unaffected.
--
-- Default FALSE gives the requested opt-in semantics for all FUTURE models. The
-- backfill below makes THIS migration behavior-neutral at deploy: it marks every
-- row MuhiyaCode could already discover as visible, so operators only ever need
-- to UNcheck the chat-only models afterwards.
--
-- The discovery filter (proxy handleModelDiscovery) lists a model when it is
-- status='active' AND NOT transcribe — it does not consult model_type. So the
-- backfill matches on exactly NOT transcribe (any status/model_type) to be a true
-- no-op: re-activating an old row later restores its prior discoverability too.

ALTER TABLE models ADD COLUMN IF NOT EXISTS muhiyacode_visible BOOLEAN NOT NULL DEFAULT false;

UPDATE models
   SET muhiyacode_visible = true
 WHERE COALESCE(transcribe, false) = false;
