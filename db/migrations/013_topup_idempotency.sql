-- 013: idempotent top-ups.
--
-- A gift-card redemption (Part G) or any retried top-up can safely resend the
-- same idem_key. The partial unique index makes the second insert a no-op
-- (ON CONFLICT DO NOTHING), so a network retry or a double-click can never
-- double-credit a user. Admin top-ups carry no idem_key (NULL) and are excluded
-- from the index, so they keep inserting freely.
ALTER TABLE user_topups ADD COLUMN IF NOT EXISTS idem_key TEXT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS user_topups_idem_key_uidx
    ON user_topups (idem_key)
    WHERE idem_key IS NOT NULL;
