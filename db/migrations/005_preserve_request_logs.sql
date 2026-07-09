-- Preserve request-log history when a user or virtual key is removed.
--
-- Before this migration, request_logs.virtual_key_id and request_logs.user_id
-- were NOT NULL with ON DELETE CASCADE. Deleting a single user in the admin UI
-- (DELETE /api/users) therefore cascade-deleted every one of that user's
-- virtual keys AND their entire request-log history in one shot - a silent,
-- unrecoverable wipe of audit data. Revoking a key is already a soft status
-- update, but a user delete was catastrophic.
--
-- Audit history must outlive the entities it references. Switch both foreign
-- keys to ON DELETE SET NULL and allow the columns to be null, so a deleted
-- user/key leaves its logs intact (merely unlinked). This is additive and
-- non-destructive: existing rows keep their values.

ALTER TABLE request_logs ALTER COLUMN virtual_key_id DROP NOT NULL;
ALTER TABLE request_logs ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_virtual_key_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_virtual_key_id_fkey
    FOREIGN KEY (virtual_key_id) REFERENCES virtual_keys(id) ON DELETE SET NULL;

ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_user_id_fkey;
ALTER TABLE request_logs ADD CONSTRAINT request_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;
