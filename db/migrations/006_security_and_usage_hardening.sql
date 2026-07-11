-- 006_security_and_usage_hardening.sql
--
-- 1. request_logs.usage_estimated: flags rows whose token/cost figures came
--    from the local word-count heuristic (upstream disconnected before its
--    usage payload arrived) rather than the provider's own numbers, so
--    estimated and provider-reported figures are never silently conflated.
--
-- 2. virtual_keys.key_hash: virtual_keys.id used to BE the bearer secret,
--    looked up directly as a primary key - so a database dump/backup leak
--    exposed every live API key verbatim. Auth now hashes the presented
--    token and looks it up by key_hash instead; existing keys keep working
--    because the Go-side backfill (db.go, backfillVirtualKeyHashes) computes
--    sha256(id) for every legacy row, and clients already send `id` as their
--    bearer token. New keys (CreateVirtualKey) generate a fresh random
--    token, store only its hash, and return the plaintext once at creation.
-- No CREATE EXTENSION needed: the hash backfill runs in Go, not SQL, so this
-- migration has no dependency on pgcrypto being installed.

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS usage_estimated BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS key_hash VARCHAR(64);
CREATE UNIQUE INDEX IF NOT EXISTS idx_virtual_keys_key_hash ON virtual_keys (key_hash);
