package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"mailmanager/internal/id"
)

func (r *Repository) ListDrafts(ctx context.Context) ([]Draft, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, identity_id, COALESCE(reply_to_message_id, ''), COALESCE(forward_message_id, ''),
		       to_json, cc_json, bcc_json, subject, body_text, body_html, version, created_at, updated_at
		FROM drafts ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	defer rows.Close()
	drafts := make([]Draft, 0)
	for rows.Next() {
		draft, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range drafts {
		if err := r.hydrateDraft(ctx, &drafts[index]); err != nil {
			return nil, err
		}
	}
	return drafts, nil
}

func (r *Repository) GetDraft(ctx context.Context, draftID string) (Draft, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, identity_id, COALESCE(reply_to_message_id, ''), COALESCE(forward_message_id, ''),
		       to_json, cc_json, bcc_json, subject, body_text, body_html, version, created_at, updated_at
		FROM drafts WHERE id = ?`, draftID)
	draft, err := scanDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	if err := r.hydrateDraft(ctx, &draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (r *Repository) CreateDraft(ctx context.Context, input DraftInput) (Draft, error) {
	if err := validateDraftInput(input); err != nil {
		return Draft{}, err
	}
	now := nowUTC()
	draft := Draft{
		ID: id.New(), AccountID: input.AccountID, IdentityID: input.IdentityID,
		ReplyToMessageID: input.ReplyToMessageID, ForwardMessageID: input.ForwardMessageID,
		To: normalizeAddresses(input.To), CC: normalizeAddresses(input.CC), BCC: normalizeAddresses(input.BCC), Subject: strings.TrimSpace(input.Subject),
		BodyText: input.BodyText, BodyHTML: input.BodyHTML, Version: 1, State: "draft",
		Attachments: []Attachment{}, CreatedAt: now, UpdatedAt: now,
	}
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		if err := validateDraftReferences(ctx, tx, input); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO drafts (
				id, account_id, identity_id, reply_to_message_id, forward_message_id,
				to_json, cc_json, bcc_json, subject, body_text, body_html, version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			draft.ID, draft.AccountID, draft.IdentityID, nullString(draft.ReplyToMessageID), nullString(draft.ForwardMessageID),
			marshalJSON(draft.To), marshalJSON(draft.CC), marshalJSON(draft.BCC), draft.Subject, draft.BodyText, draft.BodyHTML,
			unixMillis(now), unixMillis(now),
		)
		if err != nil {
			return fmt.Errorf("create draft: %w", err)
		}
		return nil
	})
	if err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (r *Repository) UpdateDraft(ctx context.Context, draftID string, input DraftInput) (Draft, error) {
	if input.Version < 1 || validateDraftInput(input) != nil {
		return Draft{}, ErrInvalid
	}
	now := nowUTC()
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		if err := validateDraftReferences(ctx, tx, input); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE drafts SET account_id = ?, identity_id = ?, reply_to_message_id = ?, forward_message_id = ?,
			       to_json = ?, cc_json = ?, bcc_json = ?, subject = ?, body_text = ?, body_html = ?,
			       version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			input.AccountID, input.IdentityID, nullString(input.ReplyToMessageID), nullString(input.ForwardMessageID),
			marshalJSON(normalizeAddresses(input.To)), marshalJSON(normalizeAddresses(input.CC)), marshalJSON(normalizeAddresses(input.BCC)), strings.TrimSpace(input.Subject),
			input.BodyText, input.BodyHTML, unixMillis(now), draftID, input.Version,
		)
		if err != nil {
			return fmt.Errorf("update draft: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 0 {
			return nil
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM drafts WHERE id = ?`, draftID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("check draft version: %w", err)
		}
		return ErrConflict
	})
	if err != nil {
		return Draft{}, err
	}
	return r.GetDraft(ctx, draftID)
}

func (r *Repository) DeleteDraft(ctx context.Context, draftID string) error {
	return r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM drafts WHERE id = ?`, draftID)
		if err != nil {
			return fmt.Errorf("delete draft: %w", err)
		}
		return requireChanged(result)
	})
}

func (r *Repository) QueueDraft(ctx context.Context, draftID string) (string, error) {
	var outboxID string
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		var accountID string
		if err := tx.QueryRowContext(ctx, `SELECT account_id FROM drafts WHERE id = ?`, draftID).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		var existingID, existingState string
		err := tx.QueryRowContext(ctx, `
			SELECT id, state FROM outbox WHERE draft_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, draftID).
			Scan(&existingID, &existingState)
		if err == nil {
			switch existingState {
			case "queued":
				outboxID = existingID
				return nil
			case "sending", "sent", "unknown":
				return ErrConflict
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check queued draft: %w", err)
		}

		now := unixMillis(nowUTC())
		outboxID = id.New()
		messageID := "<" + id.New() + "@mailmanager.local>"
		_, err = tx.ExecContext(ctx, `
			INSERT INTO outbox (id, draft_id, account_id, state, message_id, attempt_count, next_attempt_at, created_at, updated_at)
			VALUES (?, ?, ?, 'queued', ?, 0, ?, ?, ?)`, outboxID, draftID, accountID, messageID, now, now, now)
		if err != nil {
			return fmt.Errorf("queue draft: %w", err)
		}
		return nil
	})
	return outboxID, err
}

func validateDraftInput(input DraftInput) error {
	if input.AccountID == "" || input.IdentityID == "" || (input.ReplyToMessageID != "" && input.ForwardMessageID != "") {
		return ErrInvalid
	}
	for _, addresses := range [][]Address{input.To, input.CC, input.BCC} {
		for _, address := range addresses {
			email := strings.TrimSpace(address.Email)
			parsed, err := mail.ParseAddress(email)
			if err != nil || !strings.EqualFold(parsed.Address, email) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func validateDraftReferences(ctx context.Context, tx *sql.Tx, input DraftInput) error {
	var validIdentity, validReply, validForward int
	err := tx.QueryRowContext(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM identities WHERE id = ? AND account_id = ?),
			(? = '' OR EXISTS(SELECT 1 FROM messages WHERE id = ? AND account_id = ?)),
			(? = '' OR EXISTS(SELECT 1 FROM messages WHERE id = ? AND account_id = ?))`,
		input.IdentityID, input.AccountID,
		input.ReplyToMessageID, input.ReplyToMessageID, input.AccountID,
		input.ForwardMessageID, input.ForwardMessageID, input.AccountID,
	).Scan(&validIdentity, &validReply, &validForward)
	if err != nil {
		return fmt.Errorf("validate draft references: %w", err)
	}
	if validIdentity == 0 || validReply == 0 || validForward == 0 {
		return ErrInvalid
	}
	return nil
}

func scanDraft(row rowScanner) (Draft, error) {
	var draft Draft
	var toJSON, ccJSON, bccJSON string
	var createdAt, updatedAt int64
	if err := row.Scan(
		&draft.ID, &draft.AccountID, &draft.IdentityID, &draft.ReplyToMessageID, &draft.ForwardMessageID,
		&toJSON, &ccJSON, &bccJSON, &draft.Subject, &draft.BodyText, &draft.BodyHTML,
		&draft.Version, &createdAt, &updatedAt,
	); err != nil {
		return Draft{}, err
	}
	decodeAddresses(toJSON, &draft.To)
	decodeAddresses(ccJSON, &draft.CC)
	decodeAddresses(bccJSON, &draft.BCC)
	draft.CreatedAt = fromUnixMillis(createdAt)
	draft.UpdatedAt = fromUnixMillis(updatedAt)
	draft.Attachments = []Attachment{}
	draft.State = "draft"
	return draft, nil
}

func (r *Repository) hydrateDraft(ctx context.Context, draft *Draft) error {
	attachments, err := r.listDraftAttachments(ctx, draft.ID)
	if err != nil {
		return err
	}
	draft.Attachments = attachments
	draft.State = "draft"
	var state string
	err = r.db.QueryRowContext(ctx, `
		SELECT state FROM outbox WHERE draft_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, draft.ID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load draft outbox state: %w", err)
	}
	draft.State = state
	return nil
}

func marshalJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func normalizeAddresses(value []Address) []Address {
	if value == nil {
		return []Address{}
	}
	return value
}

func (r *Repository) listDraftAttachments(ctx context.Context, draftID string) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, filename, content_type, size_bytes FROM draft_attachments
		WHERE draft_id = ? ORDER BY created_at, id`, draftID)
	if err != nil {
		return nil, fmt.Errorf("list draft attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]Attachment, 0)
	for rows.Next() {
		var attachment Attachment
		if err := rows.Scan(&attachment.ID, &attachment.Filename, &attachment.ContentType, &attachment.SizeBytes); err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}
