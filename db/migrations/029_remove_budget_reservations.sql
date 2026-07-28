-- 029: remove monetary reservations completely.
--
-- Budget windows are again computed only from settled request usage. The
-- gateway prevents concurrent generations per user and constrains max_tokens
-- to the exact affordable output before contacting the provider.
DROP INDEX IF EXISTS idx_request_logs_reservation_unique;
ALTER TABLE request_logs DROP COLUMN IF EXISTS reservation_id;
DROP TABLE IF EXISTS budget_reservations;
DROP TABLE IF EXISTS user_window_charge_state;
