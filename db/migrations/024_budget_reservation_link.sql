-- 024_budget_reservation_link.sql
-- Link the immutable usage row to the admission reservation it settles.

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS reservation_id VARCHAR(100)
        REFERENCES budget_reservations(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_reservation_unique
    ON request_logs(reservation_id)
    WHERE reservation_id IS NOT NULL;
