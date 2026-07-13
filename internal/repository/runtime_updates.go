package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailmanager/internal/id"
)

func (r *Repository) UpdateOAuthToken(ctx context.Context, accountID string, encrypted []byte) error {
	if accountID == "" || len(encrypted) == 0 {
		return ErrInvalid
	}
	return r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE accounts SET oauth_token_encrypted = ?, updated_at = ? WHERE id = ?`,
			encrypted, unixMillis(nowUTC()), accountID)
		if err != nil {
			return fmt.Errorf("update OAuth token: %w", err)
		}
		return requireChanged(result)
	})
}

func (r *Repository) PrimaryIdentityID(ctx context.Context, accountID string) (string, error) {
	var identityID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM identities WHERE account_id = ? AND is_primary = 1`, accountID).Scan(&identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return identityID, err
}

func (r *Repository) OperationTargetsForConversations(ctx context.Context, conversationIDs []string) (map[string][]string, error) {
	if len(conversationIDs) == 0 {
		return nil, ErrInvalid
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(conversationIDs)), ",")
	args := make([]any, len(conversationIDs))
	for index, conversationID := range conversationIDs {
		args[index] = conversationID
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.account_id, m.id FROM conversations c
		JOIN messages m ON m.conversation_id = c.id
		WHERE c.id IN (`+placeholders+`)
		ORDER BY c.account_id, c.id, m.received_at, m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]string)
	found := make(map[string]struct{})
	for rows.Next() {
		var conversationID, accountID, messageID string
		if err := rows.Scan(&conversationID, &accountID, &messageID); err != nil {
			return nil, err
		}
		found[conversationID] = struct{}{}
		result[accountID] = append(result[accountID], messageID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) != len(conversationIDs) {
		return nil, ErrInvalid
	}
	return result, nil
}

func (r *Repository) AddDraftAttachment(ctx context.Context, draftID, filename, contentType, path string, size int64, digest []byte) (Attachment, error) {
	if draftID == "" || filename == "" || contentType == "" || path == "" || size < 0 || len(digest) == 0 {
		return Attachment{}, ErrInvalid
	}
	attachment := Attachment{ID: id.New(), Filename: filename, ContentType: contentType, SizeBytes: size}
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM drafts WHERE id = ?`, draftID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO draft_attachments (id, draft_id, filename, content_type, size_bytes, storage_path, sha256, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, attachment.ID, draftID, filename, contentType, size, path, digest, time.Now().UTC().UnixMilli())
		return err
	})
	return attachment, err
}

func (r *Repository) DeleteDraftAttachment(ctx context.Context, draftID, attachmentID string) (string, error) {
	var storagePath string
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT storage_path FROM draft_attachments WHERE id = ? AND draft_id = ?`, attachmentID, draftID).Scan(&storagePath); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM draft_attachments WHERE id = ? AND draft_id = ?`, attachmentID, draftID)
		return err
	})
	return storagePath, err
}

func (r *Repository) DraftAttachmentRecords(ctx context.Context, draftID string) ([]DraftAttachmentRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, draft_id, filename, content_type, size_bytes, storage_path, sha256
		FROM draft_attachments WHERE draft_id = ? ORDER BY created_at, id`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attachments []DraftAttachmentRecord
	for rows.Next() {
		var attachment DraftAttachmentRecord
		if err := rows.Scan(&attachment.ID, &attachment.DraftID, &attachment.Filename, &attachment.ContentType, &attachment.SizeBytes, &attachment.StoragePath, &attachment.SHA256); err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (r *Repository) SetAttachmentCache(ctx context.Context, attachmentID, path string, digest []byte) error {
	if attachmentID == "" || path == "" || len(digest) == 0 {
		return ErrInvalid
	}
	return r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE attachments SET cache_path = ?, cache_sha256 = ?, cached_at = ? WHERE id = ?`,
			path, digest, unixMillis(nowUTC()), attachmentID)
		if err != nil {
			return fmt.Errorf("cache attachment: %w", err)
		}
		return requireChanged(result)
	})
}
