-- +goose Up
ALTER TABLE sync_checkpoints ADD COLUMN body_refresh_required INTEGER NOT NULL DEFAULT 0 CHECK (body_refresh_required IN (0,1));
UPDATE sync_checkpoints SET body_refresh_required = 1;

-- +goose Down
ALTER TABLE sync_checkpoints DROP COLUMN body_refresh_required;
