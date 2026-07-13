package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (r *Repository) GetOutboxStatus(ctx context.Context, outboxID string) (OutboxStatus, error) {
	var status OutboxStatus
	var draftID, errorCode sql.NullString
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, draft_id, state, error_code, updated_at
		FROM outbox WHERE id = ?`, outboxID).Scan(
		&status.ID, &draftID, &status.Status, &errorCode, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxStatus{}, ErrNotFound
	}
	if err != nil {
		return OutboxStatus{}, fmt.Errorf("get outbox status: %w", err)
	}
	status.DraftID = draftID.String
	status.ErrorCode = errorCode.String
	status.UpdatedAt = fromUnixMillis(updatedAt)
	return status, nil
}
