package repository

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"mailmanager/internal/id"
)

var providers = map[string]bool{
	"google": true, "microsoft": true, "qq": true, "163": true, "imap": true,
}

func (r *Repository) ListAccounts(ctx context.Context) ([]AccountSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.display_name, a.email, a.provider, a.color, a.status, a.auth_type,
		       MAX(s.last_success_at), COALESCE(a.last_error_code, ''),
		       CASE WHEN COUNT(s.id) = 0 THEN 0
		            WHEN SUM(CASE WHEN s.backfill_before IS NULL THEN 1 ELSE 0 END) = COUNT(s.id) THEN 100
		            ELSE 50 END,
		       COALESCE(a.imap_host, ''), COALESCE(a.imap_port, 0),
		       COALESCE(a.smtp_host, ''), COALESCE(a.smtp_port, 0)
		FROM accounts a
		LEFT JOIN sync_checkpoints s ON s.account_id = a.id
		GROUP BY a.id
		ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]AccountSummary, 0)
	for rows.Next() {
		var account AccountSummary
		var lastSynced sql.NullInt64
		if err := rows.Scan(&account.ID, &account.DisplayName, &account.Email, &account.Provider, &account.Color, &account.Status, &account.AuthType, &lastSynced, &account.LastErrorCode, &account.BackfillProgress, &account.IMAP.Host, &account.IMAP.Port, &account.SMTP.Host, &account.SMTP.Port); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		if lastSynced.Valid {
			value := fromUnixMillis(lastSynced.Int64)
			account.LastSyncedAt = &value
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (r *Repository) GetAccount(ctx context.Context, accountID string) (AccountRecord, error) {
	var account AccountRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT id, display_name, email, provider, color, status, auth_type,
		       COALESCE(imap_host, ''), COALESCE(imap_port, 0),
		       COALESCE(smtp_host, ''), COALESCE(smtp_port, 0),
		       COALESCE(credential_encrypted, X''), COALESCE(oauth_token_encrypted, X''),
		       COALESCE(last_error_code, '')
		FROM accounts WHERE id = ?`, accountID).Scan(
		&account.ID, &account.DisplayName, &account.Email, &account.Provider, &account.Color,
		&account.Status, &account.AuthType, &account.IMAPHost, &account.IMAPPort,
		&account.SMTPHost, &account.SMTPPort, &account.CredentialEncrypted,
		&account.OAuthTokenEncrypted, &account.LastErrorCode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountRecord{}, ErrNotFound
	}
	if err != nil {
		return AccountRecord{}, fmt.Errorf("get account: %w", err)
	}
	account.AccountSummary.IMAP.Host = account.IMAPHost
	account.AccountSummary.IMAP.Port = account.IMAPPort
	account.AccountSummary.SMTP.Host = account.SMTPHost
	account.AccountSummary.SMTP.Port = account.SMTPPort
	return account, nil
}

func (r *Repository) CreateAccount(ctx context.Context, input AccountInput) (AccountSummary, error) {
	input, err := validateAccountInput(input)
	if err != nil {
		return AccountSummary{}, err
	}
	now := nowUTC()
	accountID := id.New()
	identityID := id.New()
	err = r.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (
				id, provider, display_name, email, color, status, auth_type,
				imap_host, imap_port, smtp_host, smtp_port,
				credential_encrypted, oauth_token_encrypted, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, input.Provider, input.DisplayName, input.Email, input.Color, input.AuthType,
			nullString(input.IMAPHost), nullInt(input.IMAPPort), nullString(input.SMTPHost), nullInt(input.SMTPPort),
			nullBytes(input.CredentialEncrypted), nullBytes(input.OAuthTokenEncrypted), unixMillis(now), unixMillis(now),
		)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return ErrConflict
			}
			return fmt.Errorf("insert account: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO identities (id, account_id, email, display_name, signature_html, is_primary, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, identityID, accountID, input.Email, input.DisplayName, input.SignatureHTML, unixMillis(now), unixMillis(now))
		if err != nil {
			return fmt.Errorf("insert identity: %w", err)
		}
		return nil
	})
	if err != nil {
		return AccountSummary{}, err
	}
	return AccountSummary{
		ID: accountID, DisplayName: input.DisplayName, Email: input.Email, Provider: input.Provider,
		Color: input.Color, Status: "pending", AuthType: input.AuthType,
		IMAP: AccountEndpointSummary{Host: input.IMAPHost, Port: input.IMAPPort},
		SMTP: AccountEndpointSummary{Host: input.SMTPHost, Port: input.SMTPPort},
	}, nil
}

func (r *Repository) UpdateAccountStatus(ctx context.Context, accountID, status, errorCode string) error {
	allowed := map[string]bool{"pending": true, "syncing": true, "ready": true, "error": true, "reauth_required": true, "disabled": true}
	if !allowed[status] {
		return ErrInvalid
	}
	now := unixMillis(nowUTC())
	return r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE accounts
			SET status = ?, last_error_code = NULLIF(?, ''),
			    last_error_at = CASE WHEN ? = '' THEN NULL ELSE ? END, updated_at = ?
			WHERE id = ?`, status, errorCode, errorCode, now, now, accountID)
		if err != nil {
			return fmt.Errorf("update account status: %w", err)
		}
		return requireChanged(result)
	})
}

func (r *Repository) UpdateAccountProfile(ctx context.Context, accountID, displayName, color, signatureHTML string) (AccountSummary, error) {
	displayName = strings.TrimSpace(displayName)
	color = strings.ToUpper(strings.TrimSpace(color))
	if displayName == "" || len(color) != 7 || color[0] != '#' {
		return AccountSummary{}, ErrInvalid
	}
	if _, err := hex.DecodeString(color[1:]); err != nil {
		return AccountSummary{}, ErrInvalid
	}
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE accounts SET display_name = ?, color = ?, updated_at = ? WHERE id = ?`, displayName, color, unixMillis(nowUTC()), accountID)
		if err != nil {
			return err
		}
		if err := requireChanged(result); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE identities SET display_name = ?, signature_html = CASE WHEN ? = '' THEN signature_html ELSE ? END, updated_at = ? WHERE account_id = ? AND is_primary = 1`, displayName, signatureHTML, signatureHTML, unixMillis(nowUTC()), accountID)
		return err
	})
	if err != nil {
		return AccountSummary{}, err
	}
	record, err := r.GetAccount(ctx, accountID)
	return record.AccountSummary, err
}

func (r *Repository) UpdatePasswordAccount(ctx context.Context, accountID string, input PasswordAccountUpdate) (AccountSummary, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Color = strings.ToUpper(strings.TrimSpace(input.Color))
	input.IMAPHost = strings.TrimSpace(input.IMAPHost)
	input.SMTPHost = strings.TrimSpace(input.SMTPHost)
	if input.DisplayName == "" || len(input.Color) != 7 || input.Color[0] != '#' ||
		input.IMAPHost == "" || input.IMAPPort < 1 || input.IMAPPort > 65535 ||
		input.SMTPHost == "" || input.SMTPPort < 1 || input.SMTPPort > 65535 ||
		len(input.CredentialEncrypted) == 0 {
		return AccountSummary{}, ErrInvalid
	}
	if _, err := hex.DecodeString(input.Color[1:]); err != nil {
		return AccountSummary{}, ErrInvalid
	}

	now := unixMillis(nowUTC())
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE accounts
			SET display_name = ?, color = ?, imap_host = ?, imap_port = ?,
			    smtp_host = ?, smtp_port = ?, credential_encrypted = ?,
			    status = 'pending', last_error_code = NULL, last_error_at = NULL, updated_at = ?
			WHERE id = ? AND auth_type = 'password'`,
			input.DisplayName, input.Color, input.IMAPHost, input.IMAPPort,
			input.SMTPHost, input.SMTPPort, input.CredentialEncrypted, now, accountID)
		if err != nil {
			return fmt.Errorf("update password account: %w", err)
		}
		if err := requireChanged(result); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE identities
			SET display_name = ?, signature_html = CASE WHEN ? = '' THEN signature_html ELSE ? END, updated_at = ?
			WHERE account_id = ? AND is_primary = 1`,
			input.DisplayName, input.SignatureHTML, input.SignatureHTML, now, accountID)
		if err != nil {
			return fmt.Errorf("update password account identity: %w", err)
		}
		return nil
	})
	if err != nil {
		return AccountSummary{}, err
	}
	record, err := r.GetAccount(ctx, accountID)
	return record.AccountSummary, err
}

func (r *Repository) ReplaceOAuthToken(ctx context.Context, accountID, provider, email string, encrypted []byte) (AccountSummary, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	email = strings.ToLower(strings.TrimSpace(email))
	if accountID == "" || (provider != "google" && provider != "microsoft") || email == "" || len(encrypted) == 0 {
		return AccountSummary{}, ErrInvalid
	}

	now := unixMillis(nowUTC())
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE accounts
			SET oauth_token_encrypted = ?, status = 'pending', last_error_code = NULL,
			    last_error_at = NULL, updated_at = ?
			WHERE id = ? AND provider = ? AND email = ? COLLATE NOCASE AND auth_type = 'oauth2'`,
			encrypted, now, accountID, provider, email)
		if err != nil {
			return fmt.Errorf("replace OAuth token: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return AccountSummary{}, err
	}
	record, err := r.GetAccount(ctx, accountID)
	return record.AccountSummary, err
}

func (r *Repository) DeleteAccount(ctx context.Context, accountID string) error {
	return r.writeTx(ctx, func(tx *sql.Tx) error {
		var activeWork int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM outbox WHERE account_id = ? AND state = 'sending')
			    OR EXISTS(SELECT 1 FROM operations WHERE account_id = ? AND status = 'running')`, accountID, accountID).Scan(&activeWork); err != nil {
			return fmt.Errorf("check active account work: %w", err)
		}
		if activeWork != 0 {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, accountID)
		if err != nil {
			return fmt.Errorf("delete account: %w", err)
		}
		return requireChanged(result)
	})
}

func (r *Repository) ListMailboxes(ctx context.Context, accountID string) ([]MailboxSummary, error) {
	mailboxes := make([]MailboxSummary, 0)
	if accountID == "" {
		global, err := r.listGlobalMailboxes(ctx)
		if err != nil {
			return nil, err
		}
		mailboxes = append(mailboxes, global...)
	}
	query := `
		SELECT f.id, f.account_id, f.display_name, f.remote_name, f.role, f.role_source, f.selectable,
		       COUNT(DISTINCT CASE WHEN m.seen = 0 THEN m.id END)
		FROM folders f
		LEFT JOIN message_locations ml ON ml.folder_id = f.id
		LEFT JOIN messages m ON m.id = ml.message_id
		WHERE (? = '' OR f.account_id = ?)
		GROUP BY f.id
		ORDER BY f.account_id,
		         CASE f.role WHEN 'inbox' THEN 0 WHEN 'starred' THEN 1 WHEN 'sent' THEN 2 WHEN 'drafts' THEN 3 WHEN 'archive' THEN 4 WHEN 'junk' THEN 5 WHEN 'trash' THEN 6 ELSE 7 END,
		         f.display_name COLLATE NOCASE`
	rows, err := r.db.QueryContext(ctx, query, accountID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mailbox MailboxSummary
		if err := rows.Scan(&mailbox.ID, &mailbox.AccountID, &mailbox.DisplayName, &mailbox.RemoteName, &mailbox.Role, &mailbox.RoleSource, &mailbox.Selectable, &mailbox.UnreadCount); err != nil {
			return nil, fmt.Errorf("scan mailbox: %w", err)
		}
		mailboxes = append(mailboxes, mailbox)
	}
	return mailboxes, rows.Err()
}

func (r *Repository) listGlobalMailboxes(ctx context.Context) ([]MailboxSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT '00000000-0000-7000-8000-000000000001', '', '统一收件箱', '', 'inbox', 'virtual', 1,
		       (SELECT COUNT(DISTINCT m.id) FROM messages m
		        JOIN message_locations ml ON ml.message_id = m.id JOIN folders f ON f.id = ml.folder_id
		        WHERE f.role = 'inbox' AND m.seen = 0)
		UNION ALL
		SELECT '00000000-0000-7000-8000-000000000002', '', '已加星标', '', 'starred', 'virtual', 1,
		       (SELECT COUNT(*) FROM messages m WHERE m.flagged = 1
		        AND EXISTS (SELECT 1 FROM message_locations ml WHERE ml.message_id = m.id))
		UNION ALL
		SELECT '00000000-0000-7000-8000-000000000003', '', '已发送', '', 'sent', 'virtual', 1,
		       (SELECT COUNT(DISTINCT m.id) FROM messages m
		        JOIN message_locations ml ON ml.message_id = m.id JOIN folders f ON f.id = ml.folder_id
		        WHERE f.role = 'sent' AND m.seen = 0)
		UNION ALL
		SELECT '00000000-0000-7000-8000-000000000004', '', '草稿', '', 'drafts', 'virtual', 1, 0
		UNION ALL
		SELECT '00000000-0000-7000-8000-000000000005', '', '归档', '', 'archive', 'virtual', 1,
		       (SELECT COUNT(*) FROM messages m WHERE m.seen = 0
		        AND EXISTS (SELECT 1 FROM message_locations ml JOIN folders f ON f.id = ml.folder_id
		                    WHERE ml.message_id = m.id AND f.role IN ('archive','all'))
		        AND NOT EXISTS (SELECT 1 FROM message_locations ml JOIN folders f ON f.id = ml.folder_id
		                        WHERE ml.message_id = m.id AND f.role = 'inbox'))
		UNION ALL
		SELECT '00000000-0000-7000-8000-000000000006', '', '回收站', '', 'trash', 'virtual', 1,
		       (SELECT COUNT(DISTINCT m.id) FROM messages m
		        JOIN message_locations ml ON ml.message_id = m.id JOIN folders f ON f.id = ml.folder_id
		        WHERE f.role = 'trash' AND m.seen = 0)`)
	if err != nil {
		return nil, fmt.Errorf("list global mailboxes: %w", err)
	}
	defer rows.Close()
	mailboxes := make([]MailboxSummary, 0, 6)
	for rows.Next() {
		var mailbox MailboxSummary
		if err := rows.Scan(&mailbox.ID, &mailbox.AccountID, &mailbox.DisplayName, &mailbox.RemoteName, &mailbox.Role, &mailbox.RoleSource, &mailbox.Selectable, &mailbox.UnreadCount); err != nil {
			return nil, fmt.Errorf("scan global mailbox: %w", err)
		}
		mailboxes = append(mailboxes, mailbox)
	}
	return mailboxes, rows.Err()
}

func validateAccountInput(input AccountInput) (AccountInput, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.AuthType = strings.ToLower(strings.TrimSpace(input.AuthType))
	input.Color = strings.ToUpper(strings.TrimSpace(input.Color))
	if input.DisplayName == "" || !providers[input.Provider] || (input.AuthType != "oauth2" && input.AuthType != "password") {
		return AccountInput{}, ErrInvalid
	}
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(parsed.Address, input.Email) {
		return AccountInput{}, ErrInvalid
	}
	if len(input.Color) != 7 || input.Color[0] != '#' {
		return AccountInput{}, ErrInvalid
	}
	if _, err := hex.DecodeString(input.Color[1:]); err != nil {
		return AccountInput{}, ErrInvalid
	}
	if input.AuthType == "password" && (input.IMAPHost == "" || input.SMTPHost == "" || input.IMAPPort < 1 || input.IMAPPort > 65535 || input.SMTPPort < 1 || input.SMTPPort > 65535 || len(input.CredentialEncrypted) == 0) {
		return AccountInput{}, ErrInvalid
	}
	if input.AuthType == "oauth2" && len(input.OAuthTokenEncrypted) == 0 {
		return AccountInput{}, ErrInvalid
	}
	return input, nil
}

func requireChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
