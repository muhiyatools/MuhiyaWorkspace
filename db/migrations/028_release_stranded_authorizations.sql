-- 028: clear authorizations stranded by the pre-027 settlement defect.
--
-- These rows are not usage: they are worst-case output ceilings whose request
-- logs were never committed. Keeping them "reserved" makes a user appear out
-- of credits while the usage UI correctly shows no corresponding spend.
-- A migration runs before this process accepts traffic, so no row here can
-- belong to an in-flight request in this process.
UPDATE budget_reservations
SET status = 'released', updated_at = now()
WHERE status = 'reserved';
