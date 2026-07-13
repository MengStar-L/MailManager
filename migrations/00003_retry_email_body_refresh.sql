-- +goose Up
UPDATE sync_checkpoints SET body_refresh_required = 1;

-- +goose Down
UPDATE sync_checkpoints SET body_refresh_required = 0;
