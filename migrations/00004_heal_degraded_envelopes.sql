-- +goose Up
-- Messages synced while envelope derivation failed were stored with an empty
-- subject and sender. Re-run the body refresh only for folders that contain
-- such messages so the refresh pass re-derives their metadata.
UPDATE sync_checkpoints SET body_refresh_required = 1
WHERE EXISTS (
    SELECT 1 FROM message_locations ml
    JOIN messages m ON m.id = ml.message_id
    WHERE ml.account_id = sync_checkpoints.account_id
      AND ml.folder_id = sync_checkpoints.folder_id
      AND m.subject = '' AND m.from_json = '[]'
);

-- +goose Down
SELECT 1;
