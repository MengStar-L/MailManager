package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mailmanager/internal/id"
)

type operationPayload struct {
	TargetMessageIDs []string `json:"target_message_ids"`
	DestinationID    string   `json:"destination_mailbox_id,omitempty"`
}

func (r *Repository) CreateOperation(ctx context.Context, accountID, kind string, targetIDs []string, destinationID string) (Operation, error) {
	allowed := map[string]bool{"mark_read": true, "mark_unread": true, "star": true, "unstar": true, "archive": true, "move": true, "trash": true, "delete": true}
	if accountID == "" || !allowed[kind] || len(targetIDs) == 0 || len(targetIDs) > 100 ||
		(kind == "move") != (destinationID != "") {
		return Operation{}, ErrInvalid
	}
	seen := make(map[string]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID == "" {
			return Operation{}, ErrInvalid
		}
		if _, exists := seen[targetID]; exists {
			return Operation{}, ErrInvalid
		}
		seen[targetID] = struct{}{}
	}

	placeholders := "?"
	args := []any{accountID, targetIDs[0]}
	for _, targetID := range targetIDs[1:] {
		placeholders += ",?"
		args = append(args, targetID)
	}
	now := nowUTC()
	executeAt := now
	var undoUntil *time.Time
	if kind == "archive" || kind == "trash" {
		value := now.Add(5 * time.Second)
		executeAt = value
		undoUntil = &value
	}
	operation := Operation{
		ID: id.New(), AccountID: accountID, Kind: kind, Status: "queued",
		TargetMessageIDs: append([]string(nil), targetIDs...), DestinationID: destinationID,
		UndoUntil: undoUntil, CreatedAt: now,
	}
	payload, err := json.Marshal(operationPayload{TargetMessageIDs: targetIDs, DestinationID: destinationID})
	if err != nil {
		return Operation{}, fmt.Errorf("encode operation payload: %w", err)
	}
	err = r.writeTx(ctx, func(tx *sql.Tx) error {
		var count int
		query := `SELECT COUNT(*) FROM messages WHERE account_id = ? AND id IN (` + placeholders + `)`
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return fmt.Errorf("validate operation targets: %w", err)
		}
		if count != len(targetIDs) {
			return ErrInvalid
		}
		if kind == "move" {
			var validDestination int
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS(SELECT 1 FROM folders WHERE id = ? AND account_id = ? AND selectable = 1)`,
				destinationID, accountID).Scan(&validDestination); err != nil {
				return fmt.Errorf("validate operation destination: %w", err)
			}
			if validDestination == 0 {
				return ErrInvalid
			}
		}
		if kind == "delete" {
			deleteArgs := append([]any{accountID}, args[1:]...)
			query := `SELECT COUNT(*) FROM messages m
				WHERE m.account_id = ? AND m.id IN (` + placeholders + `)
				AND EXISTS (
					SELECT 1 FROM message_locations ml
					JOIN folders f ON f.id = ml.folder_id
					WHERE ml.message_id = m.id AND f.role = 'trash'
				)`
			if err := tx.QueryRowContext(ctx, query, deleteArgs...).Scan(&count); err != nil {
				return fmt.Errorf("validate permanent delete: %w", err)
			}
			if count != len(targetIDs) {
				return ErrInvalid
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO operations (id, account_id, kind, status, payload_json, execute_after, undo_until, created_at, updated_at)
			VALUES (?, ?, ?, 'queued', ?, ?, ?, ?, ?)`,
			operation.ID, accountID, kind, string(payload), unixMillis(executeAt), nullableTime(undoUntil), unixMillis(now), unixMillis(now),
		)
		if err != nil {
			return fmt.Errorf("create operation: %w", err)
		}
		return nil
	})
	if err != nil {
		return Operation{}, err
	}
	return operation, nil
}

func (r *Repository) UndoOperation(ctx context.Context, operationID string) (Operation, error) {
	var operation Operation
	var payloadJSON string
	var createdAt int64
	var undoUntil sql.NullInt64
	var payload operationPayload
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT id, account_id, kind, status, payload_json, undo_until, created_at
			FROM operations WHERE id = ?`, operationID).Scan(
			&operation.ID, &operation.AccountID, &operation.Kind, &operation.Status,
			&payloadJSON, &undoUntil, &createdAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return fmt.Errorf("decode operation payload: %w", err)
		}
		if len(payload.TargetMessageIDs) == 0 {
			return errors.New("decode operation payload: target message IDs are missing")
		}
		now := nowUTC()
		if operation.Status != "queued" || !undoUntil.Valid || !now.Before(fromUnixMillis(undoUntil.Int64)) {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE operations SET status = 'cancelled', completed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'queued'`, unixMillis(now), unixMillis(now), operationID)
		if err != nil {
			return err
		}
		if err := requireChanged(result); err != nil {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return Operation{}, err
	}
	operation.Status = "cancelled"
	operation.TargetMessageIDs = payload.TargetMessageIDs
	operation.DestinationID = payload.DestinationID
	value := fromUnixMillis(undoUntil.Int64)
	operation.UndoUntil = &value
	operation.CreatedAt = fromUnixMillis(createdAt)
	return operation, nil
}

func (r *Repository) SystemStatus(ctx context.Context) (SystemStatus, error) {
	status := SystemStatus{Ready: true, ServerTime: time.Now().UTC()}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&status.AccountCount); err != nil {
		return SystemStatus{}, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&status.MessageCount); err != nil {
		return SystemStatus{}, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE status IN ('queued','running')`).Scan(&status.PendingActions); err != nil {
		return SystemStatus{}, err
	}
	return status, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return unixMillis(*value)
}
