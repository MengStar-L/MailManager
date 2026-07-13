package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var assignableFolderRoles = map[string]bool{
	"inbox": true, "sent": true, "drafts": true, "archive": true,
	"trash": true, "junk": true, "all": true, "other": true,
}

func (r *Repository) UpdateFolderRole(ctx context.Context, folderID, role string) (MailboxSummary, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if folderID == "" || !assignableFolderRoles[role] {
		return MailboxSummary{}, ErrInvalid
	}
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE folders SET role = ?, role_source = 'user', updated_at = ? WHERE id = ?`,
			role, unixMillis(nowUTC()), folderID)
		if err != nil {
			return fmt.Errorf("update folder role: %w", err)
		}
		return requireChanged(result)
	})
	if err != nil {
		return MailboxSummary{}, err
	}
	var mailbox MailboxSummary
	err = r.db.QueryRowContext(ctx, `
		SELECT f.id, f.account_id, f.display_name, f.remote_name, f.role, f.role_source, f.selectable,
		       COUNT(DISTINCT CASE WHEN m.seen = 0 THEN m.id END)
		FROM folders f
		LEFT JOIN message_locations ml ON ml.folder_id = f.id
		LEFT JOIN messages m ON m.id = ml.message_id
		WHERE f.id = ? GROUP BY f.id`, folderID).Scan(
		&mailbox.ID, &mailbox.AccountID, &mailbox.DisplayName, &mailbox.RemoteName,
		&mailbox.Role, &mailbox.RoleSource, &mailbox.Selectable, &mailbox.UnreadCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxSummary{}, ErrNotFound
	}
	if err != nil {
		return MailboxSummary{}, fmt.Errorf("read updated folder: %w", err)
	}
	return mailbox, nil
}
