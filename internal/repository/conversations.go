package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (r *Repository) ListConversations(ctx context.Context, query ConversationQuery) ([]ConversationSummary, *PageCursor, error) {
	hasCursorTime := !query.Before.IsZero()
	hasCursorID := query.BeforeID != ""
	if query.Limit <= 0 || query.Limit > 100 || hasCursorTime != hasCursorID {
		return nil, nil, ErrInvalid
	}
	var sqlQuery strings.Builder
	sqlQuery.WriteString(`
		SELECT c.id, c.account_id, a.display_name, a.email, a.color,
		       c.subject, c.preview, c.latest_at, c.message_count, c.unread_count, c.starred,
		       EXISTS(
		           SELECT 1 FROM messages attachment_message
		           JOIN attachments att ON att.message_id = attachment_message.id
		           WHERE attachment_message.conversation_id = c.id
		       ),
		       COALESCE((
		           SELECT latest_message.from_json FROM messages latest_message
		           WHERE latest_message.conversation_id = c.id
		           ORDER BY latest_message.received_at DESC, latest_message.id DESC LIMIT 1
		       ), '[]')
		FROM conversations c
		JOIN accounts a ON a.id = c.account_id
		WHERE 1 = 1`)
	args := make([]any, 0, 10)
	if query.AccountID != "" {
		sqlQuery.WriteString(" AND c.account_id = ?")
		args = append(args, query.AccountID)
	}
	if query.MailboxID != "" {
		sqlQuery.WriteString(` AND EXISTS (
			SELECT 1 FROM messages mailbox_message
			JOIN message_locations mailbox_location ON mailbox_location.message_id = mailbox_message.id
			WHERE mailbox_message.conversation_id = c.id AND mailbox_location.folder_id = ?
		)`)
		args = append(args, query.MailboxID)
	} else if query.MailboxRole != "" {
		if query.MailboxRole == "archive" {
			sqlQuery.WriteString(` AND EXISTS (
				SELECT 1 FROM messages role_message
				JOIN message_locations role_location ON role_location.message_id = role_message.id
				JOIN folders role_folder ON role_folder.id = role_location.folder_id
				WHERE role_message.conversation_id = c.id AND role_folder.role IN ('archive','all')
			) AND NOT EXISTS (
				SELECT 1 FROM messages inbox_message
				JOIN message_locations inbox_location ON inbox_location.message_id = inbox_message.id
				JOIN folders inbox_folder ON inbox_folder.id = inbox_location.folder_id
				WHERE inbox_message.conversation_id = c.id AND inbox_folder.role = 'inbox'
			)`)
		} else {
			sqlQuery.WriteString(` AND EXISTS (
				SELECT 1 FROM messages role_message
				JOIN message_locations role_location ON role_location.message_id = role_message.id
				JOIN folders role_folder ON role_folder.id = role_location.folder_id
				WHERE role_message.conversation_id = c.id AND role_folder.role = ?
			)`)
			args = append(args, query.MailboxRole)
		}
	} else if query.InboxOnly {
		sqlQuery.WriteString(` AND EXISTS (
			SELECT 1 FROM messages inbox_message
			JOIN message_locations inbox_location ON inbox_location.message_id = inbox_message.id
			JOIN folders inbox_folder ON inbox_folder.id = inbox_location.folder_id
			WHERE inbox_message.conversation_id = c.id AND inbox_folder.role = 'inbox'
		)`)
	}
	if query.SearchExpression != "" {
		sqlQuery.WriteString(` AND EXISTS (
			SELECT 1 FROM messages search_message
			JOIN message_search ON message_search.message_id = search_message.id
			WHERE search_message.conversation_id = c.id AND message_search MATCH ?
		)`)
		args = append(args, query.SearchExpression)
	}
	if query.UnreadOnly {
		sqlQuery.WriteString(" AND c.unread_count > 0")
	}
	if query.StarredOnly {
		sqlQuery.WriteString(" AND c.starred = 1")
	}
	if query.HasAttachments != nil {
		if *query.HasAttachments {
			sqlQuery.WriteString(` AND EXISTS (
				SELECT 1 FROM messages has_attachment_message
				JOIN attachments has_attachment ON has_attachment.message_id = has_attachment_message.id
				WHERE has_attachment_message.conversation_id = c.id
			)`)
		} else {
			sqlQuery.WriteString(` AND NOT EXISTS (
				SELECT 1 FROM messages no_attachment_message
				JOIN attachments no_attachment ON no_attachment.message_id = no_attachment_message.id
				WHERE no_attachment_message.conversation_id = c.id
			)`)
		}
	}
	if !query.Before.IsZero() && query.BeforeID != "" {
		sqlQuery.WriteString(" AND (c.latest_at < ? OR (c.latest_at = ? AND c.id < ?))")
		args = append(args, unixMillis(query.Before), unixMillis(query.Before), query.BeforeID)
	}
	sqlQuery.WriteString(" ORDER BY c.latest_at DESC, c.id DESC LIMIT ?")
	args = append(args, query.Limit+1)

	rows, err := r.db.QueryContext(ctx, sqlQuery.String(), args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	items := make([]ConversationSummary, 0, query.Limit)
	for rows.Next() {
		var item ConversationSummary
		var latestAt int64
		var participantsJSON string
		if err := rows.Scan(
			&item.ID, &item.AccountID, &item.AccountName, &item.AccountEmail, &item.AccountColor,
			&item.Subject, &item.Preview, &latestAt, &item.MessageCount, &item.UnreadCount,
			&item.Starred, &item.HasAttachments, &participantsJSON,
		); err != nil {
			return nil, nil, fmt.Errorf("scan conversation: %w", err)
		}
		item.LatestAt = fromUnixMillis(latestAt)
		decodeAddresses(participantsJSON, &item.Participants)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var next *PageCursor
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		next = &PageCursor{Timestamp: last.LatestAt, ID: last.ID}
		items = items[:query.Limit]
	}
	return items, next, nil
}

func (r *Repository) GetConversation(ctx context.Context, conversationID string) (ConversationDetail, error) {
	var detail ConversationDetail
	if err := r.db.QueryRowContext(ctx, `SELECT id, subject FROM conversations WHERE id = ?`, conversationID).Scan(&detail.ID, &detail.Subject); errors.Is(err, sql.ErrNoRows) {
		return ConversationDetail{}, ErrNotFound
	} else if err != nil {
		return ConversationDetail{}, fmt.Errorf("get conversation: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, conversation_id, account_id, from_json, to_json, cc_json, bcc_json, reply_to_json,
		       subject, sent_at, received_at, seen, flagged, body_text, body_html_clean
		FROM messages
		WHERE conversation_id = ?
		ORDER BY received_at, id`, conversationID)
	if err != nil {
		return ConversationDetail{}, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return ConversationDetail{}, err
		}
		detail.Messages = append(detail.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return ConversationDetail{}, err
	}
	if err := rows.Close(); err != nil {
		return ConversationDetail{}, err
	}
	for index := range detail.Messages {
		attachments, err := r.listAttachments(ctx, detail.Messages[index].ID)
		if err != nil {
			return ConversationDetail{}, err
		}
		detail.Messages[index].Attachments = attachments
	}
	return detail, nil
}

func (r *Repository) GetMessage(ctx context.Context, messageID string) (MessageDetail, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, conversation_id, account_id, from_json, to_json, cc_json, bcc_json, reply_to_json,
		       subject, sent_at, received_at, seen, flagged, body_text, body_html_clean
		FROM messages WHERE id = ?`, messageID)
	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageDetail{}, ErrNotFound
	}
	if err != nil {
		return MessageDetail{}, err
	}
	message.Attachments, err = r.listAttachments(ctx, message.ID)
	return message, err
}

func (r *Repository) GetAttachment(ctx context.Context, attachmentID string) (AttachmentRecord, error) {
	var record AttachmentRecord
	var disposition string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, message_id, part_id, filename, content_type, size_bytes, disposition, COALESCE(cache_path, '')
		FROM attachments WHERE id = ?`, attachmentID).Scan(
		&record.ID, &record.MessageID, &record.PartID, &record.Filename,
		&record.ContentType, &record.SizeBytes, &disposition, &record.CachePath,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return AttachmentRecord{}, fmt.Errorf("get attachment: %w", err)
	}
	record.Inline = disposition == "inline"
	return record, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (MessageDetail, error) {
	var message MessageDetail
	var fromJSON, toJSON, ccJSON, bccJSON, replyToJSON string
	var sentAt sql.NullInt64
	var receivedAt int64
	if err := row.Scan(
		&message.ID, &message.ConversationID, &message.AccountID,
		&fromJSON, &toJSON, &ccJSON, &bccJSON, &replyToJSON,
		&message.Subject, &sentAt, &receivedAt, &message.Seen, &message.Starred,
		&message.BodyText, &message.BodyHTML,
	); err != nil {
		return MessageDetail{}, err
	}
	decodeAddresses(fromJSON, &message.From)
	decodeAddresses(toJSON, &message.To)
	decodeAddresses(ccJSON, &message.CC)
	decodeAddresses(bccJSON, &message.BCC)
	decodeAddresses(replyToJSON, &message.ReplyTo)
	if sentAt.Valid {
		value := fromUnixMillis(sentAt.Int64)
		message.SentAt = &value
	}
	message.ReceivedAt = fromUnixMillis(receivedAt)
	message.RemoteImagesBlocked = strings.Contains(message.BodyHTML, "data-mm-remote-src")
	return message, nil
}

func (r *Repository) listAttachments(ctx context.Context, messageID string) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, filename, content_type, size_bytes, disposition
		FROM attachments WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]Attachment, 0)
	for rows.Next() {
		var attachment Attachment
		var disposition string
		if err := rows.Scan(&attachment.ID, &attachment.Filename, &attachment.ContentType, &attachment.SizeBytes, &disposition); err != nil {
			return nil, err
		}
		attachment.Inline = disposition == "inline"
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func decodeAddresses(raw string, target *[]Address) {
	if err := json.Unmarshal([]byte(raw), target); err != nil || *target == nil {
		*target = []Address{}
	}
}
