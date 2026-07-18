-- 017_activate_gemma_vision.sql: activate the seeded free vision model, but
-- ONLY when the operator has already activated and keyed the OpenRouter
-- provider. This closes the "image request → no active vision model → routing
-- error" gap on deployments where OpenRouter is live, without silently turning
-- on a $0 model on a fresh install that has no key yet. The free-model capture
-- risk is contained separately by preferPaid (a $0 model never becomes the
-- default text route), so auto-activation here is safe.
--
-- Idempotent: only flips rows that are still inactive, and no-ops when the
-- provider is missing/inactive/unkeyed. Operators on any other setup use the
-- admin coverage banner + Vision checkbox to activate their own vision model.

UPDATE models
SET status = 'active'
WHERE name = 'gemma-4-vision'
  AND status = 'inactive'
  AND EXISTS (
    SELECT 1 FROM providers
    WHERE id = 'openrouter'
      AND status = 'active'
      AND COALESCE(api_key, '') <> ''
  );
