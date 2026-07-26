package ingeststore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/search"
	"mailmanager/internal/store"
	mailSync "mailmanager/internal/sync"
)

type Store struct {
	database *store.Store
}

type FolderRecord struct {
	ID         string
	AccountID  string
	RemoteName string
	Role       string
	RoleSource string
	Selectable bool
}

const maximumFolderSnapshotUIDs = 100000

func New(database *store.Store) *Store {
	return &Store{database: database}
}

func (s *Store) UpsertFolders(ctx context.Context, accountID string, mailboxes []connectors.RemoteMailbox) ([]FolderRecord, error) {
	if accountID == "" {
		return nil, errors.New("account ID is required")
	}
	now := time.Now().UTC().UnixMilli()
	backfillBefore := time.Now().UTC().Add(-90 * 24 * time.Hour).UnixMilli()
	result := make([]FolderRecord, 0, len(mailboxes))
	err := s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, mailbox := range mailboxes {
			if strings.TrimSpace(mailbox.Name) == "" {
				continue
			}
			role := string(mailbox.Role)
			roleSource := string(mailbox.RoleSource)
			if role == string(accounts.FolderUnknown) || role == "" {
				role = "other"
				roleSource = string(accounts.RoleNeedsUser)
			} else if roleSource == "" {
				roleSource = string(accounts.RoleFromPreset)
			}
			var folderID string
			err := tx.QueryRowContext(ctx, `SELECT id FROM folders WHERE account_id = ? AND remote_name = ?`, accountID, mailbox.Name).Scan(&folderID)
			if errors.Is(err, sql.ErrNoRows) {
				folderID, err = store.NewID()
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `
					INSERT INTO folders (id, account_id, remote_name, display_name, delimiter, role, role_source, selectable, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					folderID, accountID, mailbox.Name, mailbox.Name, nullableDelimiter(mailbox.Delimiter), role, roleSource, mailbox.Selectable, now, now)
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `
					UPDATE folders SET delimiter = ?,
					    role = CASE WHEN role_source = 'user' THEN role ELSE ? END,
					    role_source = CASE WHEN role_source = 'user' THEN role_source ELSE ? END,
					    selectable = ?, updated_at = ? WHERE id = ?`,
					nullableDelimiter(mailbox.Delimiter), role, roleSource, mailbox.Selectable, now, folderID)
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO sync_checkpoints (id, account_id, folder_id, backfill_before, state, updated_at)
				VALUES (?, ?, ?, ?, 'idle', ?)
				ON CONFLICT(account_id, folder_id) DO NOTHING`, mustID(), accountID, folderID, backfillBefore, now); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT role, role_source FROM folders WHERE id = ?`, folderID).Scan(&role, &roleSource); err != nil {
				return err
			}
			result = append(result, FolderRecord{ID: folderID, AccountID: accountID, RemoteName: mailbox.Name, Role: role, RoleSource: roleSource, Selectable: mailbox.Selectable})
		}
		return nil
	})
	return result, err
}

func (s *Store) Folders(ctx context.Context, accountID string) ([]FolderRecord, error) {
	rows, err := s.database.DB().QueryContext(ctx, `
		SELECT id, account_id, remote_name, role, role_source, selectable FROM folders
		WHERE account_id = ? AND selectable = 1
		ORDER BY CASE role WHEN 'inbox' THEN 0 WHEN 'sent' THEN 1 WHEN 'archive' THEN 2 ELSE 3 END, remote_name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folders []FolderRecord
	for rows.Next() {
		var folder FolderRecord
		if err := rows.Scan(&folder.ID, &folder.AccountID, &folder.RemoteName, &folder.Role, &folder.RoleSource, &folder.Selectable); err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}
	return folders, rows.Err()
}

func (s *Store) Checkpoint(ctx context.Context, accountID, folderID string) (mailSync.Checkpoint, error) {
	checkpoint := mailSync.Checkpoint{AccountID: accountID, FolderID: folderID}
	var uidValidity, uidNext, lastUID, highestModSeq sql.NullInt64
	var bodyRefreshRequired bool
	var updatedAt int64
	err := s.database.DB().QueryRowContext(ctx, `
		SELECT uid_validity, COALESCE((SELECT uid_next FROM folders WHERE id = folder_id), 0), last_uid, highest_modseq,
		       body_refresh_required, updated_at
		FROM sync_checkpoints WHERE account_id = ? AND folder_id = ?`, accountID, folderID).Scan(
		&uidValidity, &uidNext, &lastUID, &highestModSeq, &bodyRefreshRequired, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint, nil
	}
	if err != nil {
		return mailSync.Checkpoint{}, err
	}
	checkpoint.UIDValidity = uint32(uidValidity.Int64)
	checkpoint.UIDNext = uint32(uidNext.Int64)
	checkpoint.LastUID = uint32(lastUID.Int64)
	checkpoint.HighestModSeq = uint64(highestModSeq.Int64)
	checkpoint.BodyRefreshRequired = bodyRefreshRequired
	checkpoint.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return checkpoint, nil
}

func (s *Store) PendingOperationIDs(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.database.DB().QueryContext(ctx, `SELECT id FROM operations WHERE account_id = ? AND status IN ('queued','running')`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) NeedsBackfill(ctx context.Context, accountID, folderID string) (bool, error) {
	var pending int
	err := s.database.DB().QueryRowContext(ctx, `
		SELECT backfill_before IS NOT NULL FROM sync_checkpoints WHERE account_id = ? AND folder_id = ?`,
		accountID, folderID).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return pending != 0, err
}

func (s *Store) CompleteBackfill(ctx context.Context, accountID, folderID string) error {
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE sync_checkpoints SET backfill_before = NULL, state = 'idle', updated_at = ?
			WHERE account_id = ? AND folder_id = ?`, time.Now().UTC().UnixMilli(), accountID, folderID)
		return err
	})
}

func (s *Store) PrepareFolder(ctx context.Context, decision mailSync.ReconcileDecision) error {
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		if decision.Action == mailSync.ReconcileRebuild {
			affected, err := folderConversationIDs(ctx, tx, decision.Checkpoint.AccountID, decision.Checkpoint.FolderID)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM message_locations WHERE account_id = ? AND folder_id = ?`, decision.Checkpoint.AccountID, decision.Checkpoint.FolderID); err != nil {
				return err
			}
			if err := cleanupOrphanMessages(ctx, tx, decision.Checkpoint.AccountID); err != nil {
				return err
			}
			for conversationID := range affected {
				if err := refreshConversation(ctx, tx, conversationID); err != nil {
					return err
				}
			}
			for _, operationID := range decision.OperationsNeedingAttention {
				if _, err := tx.ExecContext(ctx, `UPDATE operations SET status = 'needs_attention', updated_at = ? WHERE id = ? AND status IN ('queued','running')`, time.Now().UTC().UnixMilli(), operationID); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE sync_checkpoints SET uid_validity = ?, state = 'syncing', updated_at = ?
			WHERE account_id = ? AND folder_id = ?`,
			decision.Checkpoint.UIDValidity, decision.Checkpoint.UpdatedAt.UnixMilli(), decision.Checkpoint.AccountID, decision.Checkpoint.FolderID)
		return err
	})
}

func (s *Store) ReconcileFolder(ctx context.Context, snapshot mailSync.FolderSnapshot) error {
	states, remoteUIDsJSON, err := validateFolderSnapshot(snapshot)
	if err != nil {
		return err
	}
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		var folderExists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE id = ? AND account_id = ?`, snapshot.FolderID, snapshot.AccountID).Scan(&folderExists)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("folder does not belong to the snapshot account")
		}
		if err != nil {
			return err
		}

		type localState struct {
			locationID     string
			messageID      string
			conversationID string
			uidValidity    uint32
			uid            uint32
			flagsJSON      string
			seen           bool
			flagged        bool
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT ml.id, ml.message_id, m.conversation_id, ml.uid_validity, ml.uid,
			       ml.flags_json, m.seen, m.flagged
			FROM message_locations ml
			JOIN messages m ON m.id = ml.message_id
			WHERE ml.account_id = ? AND ml.folder_id = ?`, snapshot.AccountID, snapshot.FolderID)
		if err != nil {
			return err
		}
		var locals []localState
		for rows.Next() {
			var local localState
			if err := rows.Scan(
				&local.locationID, &local.messageID, &local.conversationID,
				&local.uidValidity, &local.uid, &local.flagsJSON, &local.seen, &local.flagged,
			); err != nil {
				_ = rows.Close()
				return err
			}
			locals = append(locals, local)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		now := time.Now().UTC().UnixMilli()
		affected := make(map[string]struct{})
		for _, local := range locals {
			remote, present := states[local.uid]
			if local.uidValidity != snapshot.UIDValidity || !present {
				affected[local.conversationID] = struct{}{}
				continue
			}
			remoteFlags := canonicalFlags(remote.Flags)
			flagsJSON := marshal(remoteFlags)
			seen, flagged := flags(remoteFlags)
			if local.flagsJSON == flagsJSON && local.seen == seen && local.flagged == flagged {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE message_locations SET flags_json = ?, updated_at = ?
				WHERE id = ? AND account_id = ? AND folder_id = ?`,
				flagsJSON, now, local.locationID, snapshot.AccountID, snapshot.FolderID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET seen = ?, flagged = ?, updated_at = ?
				WHERE id = ? AND account_id = ?`, seen, flagged, now, local.messageID, snapshot.AccountID); err != nil {
				return err
			}
			affected[local.conversationID] = struct{}{}
		}

		// Rows at or above the snapshot's SELECT-time UIDNEXT were stored by
		// a concurrent incremental sync after the snapshot was captured;
		// their absence from it is not a deletion.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM message_locations
			WHERE account_id = ? AND folder_id = ?
			  AND (uid_validity <> ? OR (uid < ? AND NOT EXISTS (
				SELECT 1 FROM json_each(?) remote
				WHERE CAST(remote.value AS INTEGER) = message_locations.uid
			  )))`, snapshot.AccountID, snapshot.FolderID, snapshot.UIDValidity, snapshot.UIDNext, remoteUIDsJSON); err != nil {
			return err
		}
		if err := cleanupOrphanMessages(ctx, tx, snapshot.AccountID); err != nil {
			return err
		}
		for conversationID := range affected {
			if err := refreshConversation(ctx, tx, conversationID); err != nil {
				return err
			}
		}
		return nil
	})
}

func validateFolderSnapshot(snapshot mailSync.FolderSnapshot) (map[uint32]connectors.RemoteMessageState, string, error) {
	if strings.TrimSpace(snapshot.AccountID) == "" || strings.TrimSpace(snapshot.FolderID) == "" {
		return nil, "", errors.New("folder snapshot account and folder are required")
	}
	if snapshot.UIDValidity == 0 {
		return nil, "", errors.New("folder snapshot UIDVALIDITY is required")
	}
	if snapshot.UIDNext == 0 {
		return nil, "", errors.New("folder snapshot UIDNEXT is required")
	}
	if len(snapshot.RemoteUIDs) > maximumFolderSnapshotUIDs {
		return nil, "", fmt.Errorf("folder snapshot cannot exceed %d UIDs", maximumFolderSnapshotUIDs)
	}
	remoteUIDs := make(map[uint32]struct{}, len(snapshot.RemoteUIDs))
	for _, uid := range snapshot.RemoteUIDs {
		if uid == 0 {
			return nil, "", errors.New("folder snapshot UIDs must be non-zero")
		}
		if _, duplicate := remoteUIDs[uid]; duplicate {
			return nil, "", fmt.Errorf("folder snapshot contains duplicate UID %d", uid)
		}
		remoteUIDs[uid] = struct{}{}
	}
	if len(snapshot.States) != len(remoteUIDs) {
		return nil, "", fmt.Errorf("folder snapshot has %d UIDs but %d states", len(remoteUIDs), len(snapshot.States))
	}
	states := make(map[uint32]connectors.RemoteMessageState, len(snapshot.States))
	for _, state := range snapshot.States {
		if state.UID == 0 {
			return nil, "", errors.New("folder snapshot state UIDs must be non-zero")
		}
		if _, ok := remoteUIDs[state.UID]; !ok {
			return nil, "", fmt.Errorf("folder snapshot state UID %d is not in the remote UID set", state.UID)
		}
		if _, duplicate := states[state.UID]; duplicate {
			return nil, "", fmt.Errorf("folder snapshot contains duplicate state UID %d", state.UID)
		}
		states[state.UID] = state
	}
	encoded, err := json.Marshal(snapshot.RemoteUIDs)
	if err != nil {
		return nil, "", err
	}
	return states, string(encoded), nil
}

func folderConversationIDs(ctx context.Context, tx *sql.Tx, accountID, folderID string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT m.conversation_id
		FROM message_locations ml
		JOIN messages m ON m.id = ml.message_id
		WHERE ml.account_id = ? AND ml.folder_id = ?`, accountID, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]struct{})
	for rows.Next() {
		var conversationID string
		if err := rows.Scan(&conversationID); err != nil {
			return nil, err
		}
		result[conversationID] = struct{}{}
	}
	return result, rows.Err()
}

func cleanupOrphanMessages(ctx context.Context, tx *sql.Tx, accountID string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM message_search
		WHERE message_id IN (
			SELECT m.id FROM messages m
			WHERE m.account_id = ?
			  AND NOT EXISTS (SELECT 1 FROM message_locations ml WHERE ml.message_id = m.id)
		)`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM messages
		WHERE account_id = ?
		  AND NOT EXISTS (SELECT 1 FROM message_locations ml WHERE ml.message_id = messages.id)`, accountID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM conversations
		WHERE account_id = ?
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = conversations.id)`, accountID)
	return err
}

func (s *Store) StoreMessages(ctx context.Context, batch mailSync.MessageBatch) error {
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		affected := make(map[string]struct{})
		for _, remote := range batch.Messages {
			conversationID, err := s.storeMessage(ctx, tx, batch, remote)
			if err != nil {
				return err
			}
			affected[conversationID] = struct{}{}
		}
		for conversationID := range affected {
			if err := refreshConversation(ctx, tx, conversationID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) CompleteFolder(ctx context.Context, checkpoint mailSync.Checkpoint) error {
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		// last_uid must never move backwards within a UIDVALIDITY generation:
		// a long reconcile completing after concurrent incremental syncs
		// would otherwise rewind the checkpoint and force refetches.
		_, err := tx.ExecContext(ctx, `
			UPDATE sync_checkpoints SET
			       last_uid = CASE WHEN uid_validity = ? THEN MAX(COALESCE(last_uid, 0), ?) ELSE ? END,
			       uid_validity = ?, highest_modseq = ?, body_refresh_required = ?, state = 'idle',
			       last_success_at = ?, last_error_code = NULL, updated_at = ?
			WHERE account_id = ? AND folder_id = ?`,
			checkpoint.UIDValidity, checkpoint.LastUID, checkpoint.LastUID,
			checkpoint.UIDValidity, checkpoint.HighestModSeq, checkpoint.BodyRefreshRequired,
			checkpoint.UpdatedAt.UnixMilli(), checkpoint.UpdatedAt.UnixMilli(), checkpoint.AccountID, checkpoint.FolderID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE folders SET
			       uid_next = CASE WHEN uid_validity = ? THEN MAX(COALESCE(uid_next, 0), ?) ELSE ? END,
			       uid_validity = ?, highest_modseq = ?, updated_at = ?
			WHERE id = ?`,
			checkpoint.UIDValidity, checkpoint.UIDNext, checkpoint.UIDNext,
			checkpoint.UIDValidity, checkpoint.HighestModSeq, checkpoint.UpdatedAt.UnixMilli(), checkpoint.FolderID)
		return err
	})
}

func (s *Store) MarkFolderError(ctx context.Context, accountID, folderID, code string) error {
	return s.database.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sync_checkpoints SET state = 'error', last_error_code = ?, updated_at = ? WHERE account_id = ? AND folder_id = ?`, code, time.Now().UTC().UnixMilli(), accountID, folderID)
		return err
	})
}

func (s *Store) storeMessage(ctx context.Context, tx *sql.Tx, batch mailSync.MessageBatch, remote connectors.RemoteMessage) (string, error) {
	if remote.UID == 0 || batch.UIDValidity == 0 {
		return "", errors.New("remote message location is incomplete")
	}
	gmailMessageID := ""
	if batch.Provider == accounts.ProviderGoogle {
		if remote.GmailMessageID == 0 {
			return "", errors.New("Google message is missing X-GM-MSGID")
		}
		gmailMessageID = strconv.FormatUint(remote.GmailMessageID, 10)
	}
	var existingMessageID, existingConversationID, existingGmailMessageID string
	err := tx.QueryRowContext(ctx, `
		SELECT ml.message_id, m.conversation_id, COALESCE(m.gmail_message_id, '') FROM message_locations ml
		JOIN messages m ON m.id = ml.message_id
		WHERE ml.account_id = ? AND ml.folder_id = ? AND ml.uid_validity = ? AND ml.uid = ?`,
		batch.AccountID, batch.FolderID, batch.UIDValidity, remote.UID).Scan(
		&existingMessageID, &existingConversationID, &existingGmailMessageID)
	if err == nil {
		if gmailMessageID != "" && existingGmailMessageID != "" && existingGmailMessageID != gmailMessageID {
			return "", errors.New("remote location changed X-GM-MSGID")
		}
		seen, starred := flags(remote.Flags)
		if batch.RefreshBody {
			now := time.Now().UTC().UnixMilli()
			searchTokens := search.IndexText(remote.Envelope.Subject + " " + remote.TextBody)
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET body_text = ?, body_html_clean = ?, search_tokens = ?,
				       seen = ?, flagged = ?, updated_at = ? WHERE id = ?`,
				remote.TextBody, remote.HTMLBody, searchTokens, seen, starred, now, existingMessageID); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE message_search SET body_tokens = ? WHERE message_id = ?`,
				search.IndexText(remote.TextBody), existingMessageID); err != nil {
				return "", err
			}
			// A refresh also re-derives envelope metadata, but only with a
			// non-empty envelope: overwriting good rows with a failed header
			// parse would repeat the damage this pass exists to heal.
			if remote.Envelope.Subject != "" || len(remote.Envelope.From) > 0 {
				preview := compactPreview(firstNonEmpty(remote.TextBody, remote.Envelope.Subject))
				if _, err := tx.ExecContext(ctx, `
					UPDATE messages SET subject = ?, from_json = ?, to_json = ?, cc_json = ?, reply_to_json = ?,
					       sent_at = ?, preview = ?, updated_at = ? WHERE id = ?`,
					remote.Envelope.Subject, addressesJSON(remote.Envelope.From), addressesJSON(remote.Envelope.To),
					addressesJSON(remote.Envelope.Cc), addressesJSON(remote.Envelope.ReplyTo),
					nullTime(remote.Envelope.Date), preview, now, existingMessageID); err != nil {
					return "", err
				}
				if _, err := tx.ExecContext(ctx, `
					UPDATE message_search SET subject_tokens = ?, address_tokens = ? WHERE message_id = ?`,
					search.IndexText(remote.Envelope.Subject),
					search.IndexText(addressText(remote.Envelope.From, remote.Envelope.To, remote.Envelope.Cc)),
					existingMessageID); err != nil {
					return "", err
				}
				if err := refreshConversation(ctx, tx, existingConversationID); err != nil {
					return "", err
				}
			}
			return existingConversationID, nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE messages SET gmail_message_id = COALESCE(gmail_message_id, NULLIF(?, '')),
			       seen = ?, flagged = ?, updated_at = ? WHERE id = ?`,
			gmailMessageID, seen, starred, time.Now().UTC().UnixMilli(), existingMessageID); err != nil {
			return "", err
		}
		// Heal envelope metadata an earlier degraded header parse stored
		// empty once a later fetch derives it.
		if remote.Envelope.Subject != "" || len(remote.Envelope.From) > 0 {
			result, err := tx.ExecContext(ctx, `
				UPDATE messages SET subject = ?, from_json = ?, to_json = ?, cc_json = ?, reply_to_json = ?,
				       sent_at = ?, updated_at = ?
				WHERE id = ? AND subject = '' AND from_json = '[]'`,
				remote.Envelope.Subject, addressesJSON(remote.Envelope.From), addressesJSON(remote.Envelope.To),
				addressesJSON(remote.Envelope.Cc), addressesJSON(remote.Envelope.ReplyTo),
				nullTime(remote.Envelope.Date), time.Now().UTC().UnixMilli(), existingMessageID)
			if err != nil {
				return "", err
			}
			if healed, err := result.RowsAffected(); err == nil && healed > 0 {
				if _, err := tx.ExecContext(ctx, `
					UPDATE message_search SET subject_tokens = ?, address_tokens = ? WHERE message_id = ?`,
					search.IndexText(remote.Envelope.Subject),
					search.IndexText(addressText(remote.Envelope.From, remote.Envelope.To, remote.Envelope.Cc)),
					existingMessageID); err != nil {
					return "", err
				}
				if err := refreshConversation(ctx, tx, existingConversationID); err != nil {
					return "", err
				}
			}
		}
		// Heal a message an earlier degraded sync attempt stored without a
		// body once a later fetch delivers one.
		if remote.TextBody != "" || remote.HTMLBody != "" {
			result, err := tx.ExecContext(ctx, `
				UPDATE messages SET body_text = ?, body_html_clean = ?, search_tokens = ?, updated_at = ?
				WHERE id = ? AND body_text = '' AND body_html_clean = ''`,
				remote.TextBody, remote.HTMLBody, search.IndexText(remote.Envelope.Subject+" "+remote.TextBody),
				time.Now().UTC().UnixMilli(), existingMessageID)
			if err != nil {
				return "", err
			}
			if healed, err := result.RowsAffected(); err == nil && healed > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE message_search SET body_tokens = ? WHERE message_id = ?`,
					search.IndexText(remote.TextBody), existingMessageID); err != nil {
					return "", err
				}
			}
		}
		return existingConversationID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	header := parseHeader(remote.Header)
	messageID := firstNonEmpty(remote.Envelope.MessageID, header.messageID)
	references := header.references
	if len(references) == 0 {
		references = append(references, remote.Envelope.InReplyTo...)
	}
	receivedAt := remote.InternalDate.UTC()
	if receivedAt.IsZero() {
		receivedAt = remote.Envelope.Date.UTC()
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	threadKey := chooseThreadKey(messageID, references, batch.FolderID, batch.UIDValidity, remote.UID)
	var localMessageID, conversationID string
	reusedGmailMessage := false
	if gmailMessageID != "" {
		err := tx.QueryRowContext(ctx, `
			SELECT id, conversation_id FROM messages
			WHERE account_id = ? AND gmail_message_id = ?`, batch.AccountID, gmailMessageID).
			Scan(&localMessageID, &conversationID)
		reusedGmailMessage = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if localMessageID == "" && messageID != "" {
		err := tx.QueryRowContext(ctx, `
			SELECT m.id, m.conversation_id FROM messages m
			WHERE m.account_id = ? AND m.rfc_message_id = ? AND m.received_at = ? AND m.size_bytes = ?
			  AND NOT EXISTS (SELECT 1 FROM message_locations ml WHERE ml.message_id = m.id)
			LIMIT 1`, batch.AccountID, messageID, receivedAt.UnixMilli(), remote.RFC822Size).
			Scan(&localMessageID, &conversationID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if conversationID == "" {
		conversationID, err = findConversation(ctx, tx, batch.AccountID, references, threadKey)
		if err != nil {
			return "", err
		}
	}
	preview := compactPreview(firstNonEmpty(remote.TextBody, remote.Envelope.Subject))
	if conversationID == "" {
		conversationID = mustID()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO conversations (id, account_id, thread_key, subject, preview, latest_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, conversationID, batch.AccountID, threadKey,
			remote.Envelope.Subject, preview, receivedAt.UnixMilli(), time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli())
		if err != nil {
			return "", err
		}
	}
	if localMessageID == "" {
		localMessageID = mustID()
	}
	if reusedGmailMessage {
		if err := storeMessageLocation(ctx, tx, batch, remote, localMessageID); err != nil {
			return "", err
		}
		return conversationID, nil
	}
	seen, starred := flags(remote.Flags)
	fromJSON := addressesJSON(remote.Envelope.From)
	toJSON := addressesJSON(remote.Envelope.To)
	ccJSON := addressesJSON(remote.Envelope.Cc)
	replyToJSON := addressesJSON(remote.Envelope.ReplyTo)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages (
			id, account_id, conversation_id, rfc_message_id, in_reply_to, references_json, gmail_message_id,
			subject, from_json, to_json, cc_json, bcc_json, reply_to_json,
			sent_at, received_at, preview, body_text, body_html_clean, search_tokens,
			seen, flagged, size_bytes, created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET conversation_id = excluded.conversation_id, subject = excluded.subject,
			gmail_message_id = COALESCE(messages.gmail_message_id, excluded.gmail_message_id),
			body_text = excluded.body_text, body_html_clean = excluded.body_html_clean,
			seen = excluded.seen, flagged = excluded.flagged, updated_at = excluded.updated_at`,
		localMessageID, batch.AccountID, conversationID, messageID, firstReference(remote.Envelope.InReplyTo), marshal(references),
		gmailMessageID, remote.Envelope.Subject, fromJSON, toJSON, ccJSON, replyToJSON,
		nullTime(remote.Envelope.Date), receivedAt.UnixMilli(), preview, remote.TextBody, remote.HTMLBody,
		search.IndexText(remote.Envelope.Subject+" "+remote.TextBody), seen, starred, remote.RFC822Size,
		time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli(),
	); err != nil {
		return "", err
	}
	if err := storeMessageLocation(ctx, tx, batch, remote, localMessageID); err != nil {
		return "", err
	}
	if err := replaceAttachments(ctx, tx, localMessageID, remote.Parts); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_search WHERE message_id = ?`, localMessageID); err != nil {
		return "", err
	}
	addressTokens := search.IndexText(addressText(remote.Envelope.From, remote.Envelope.To, remote.Envelope.Cc))
	_, err = tx.ExecContext(ctx, `
		INSERT INTO message_search (message_id, account_id, subject_tokens, address_tokens, body_tokens)
		VALUES (?, ?, ?, ?, ?)`, localMessageID, batch.AccountID,
		search.IndexText(remote.Envelope.Subject), addressTokens, search.IndexText(remote.TextBody))
	return conversationID, err
}

func storeMessageLocation(ctx context.Context, tx *sql.Tx, batch mailSync.MessageBatch, remote connectors.RemoteMessage, messageID string) error {
	now := time.Now().UTC().UnixMilli()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO message_locations (id, message_id, account_id, folder_id, uid_validity, uid, flags_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, folder_id, uid_validity, uid) DO UPDATE SET flags_json = excluded.flags_json, updated_at = excluded.updated_at`,
		mustID(), messageID, batch.AccountID, batch.FolderID, batch.UIDValidity, remote.UID,
		marshal(remote.Flags), now, now)
	return err
}

func findConversation(ctx context.Context, tx *sql.Tx, accountID string, references []string, threadKey string) (string, error) {
	for index := len(references) - 1; index >= 0; index-- {
		var conversationID string
		err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM messages WHERE account_id = ? AND rfc_message_id = ? LIMIT 1`, accountID, references[index]).Scan(&conversationID)
		if err == nil {
			return conversationID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	var conversationID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM conversations WHERE account_id = ? AND thread_key = ?`, accountID, threadKey).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return conversationID, err
}

func refreshConversation(ctx context.Context, tx *sql.Tx, conversationID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE conversations SET
			subject = COALESCE((SELECT subject FROM messages WHERE conversation_id = ? ORDER BY received_at DESC, id DESC LIMIT 1), ''),
			preview = COALESCE((SELECT preview FROM messages WHERE conversation_id = ? ORDER BY received_at DESC, id DESC LIMIT 1), ''),
			latest_at = COALESCE((SELECT MAX(received_at) FROM messages WHERE conversation_id = ?), latest_at),
			message_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?),
			unread_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND seen = 0),
			starred = EXISTS(SELECT 1 FROM messages WHERE conversation_id = ? AND flagged = 1),
			updated_at = ?
		WHERE id = ?`, conversationID, conversationID, conversationID, conversationID,
		conversationID, conversationID, time.Now().UTC().UnixMilli(), conversationID)
	return err
}

func replaceAttachments(ctx context.Context, tx *sql.Tx, messageID string, parts []connectors.RemotePart) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE message_id = ?`, messageID); err != nil {
		return err
	}
	for _, part := range parts {
		mediaType := strings.ToLower(part.MediaType)
		if part.Disposition != "attachment" && part.Filename == "" && (mediaType == "text/plain" || mediaType == "text/html") {
			continue
		}
		disposition := "attachment"
		if strings.EqualFold(part.Disposition, "inline") {
			disposition = "inline"
		}
		path := partPath(part.Path)
		if path == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO attachments (id, message_id, part_id, filename, content_type, content_id, disposition, size_bytes, created_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`, mustID(), messageID, path,
			connectors.SanitizeFilename(part.Filename), firstNonEmpty(part.MediaType, "application/octet-stream"),
			part.ContentID, disposition, part.Size, time.Now().UTC().UnixMilli())
		if err != nil {
			return err
		}
	}
	return nil
}

type parsedHeader struct {
	messageID  string
	references []string
}

var messageIDPattern = regexp.MustCompile(`<[^<>\s]+>`)

func parseHeader(raw []byte) parsedHeader {
	if len(raw) == 0 {
		return parsedHeader{}
	}
	message, err := mail.ReadMessage(bytes.NewReader(append(append([]byte(nil), raw...), '\r', '\n', '\r', '\n')))
	if err != nil {
		return parsedHeader{}
	}
	return parsedHeader{messageID: normalizeMessageID(message.Header.Get("Message-ID")), references: normalizedMessageIDs(message.Header.Get("References"))}
}

func normalizedMessageIDs(value string) []string {
	matches := messageIDPattern.FindAllString(value, -1)
	result := make([]string, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		normalized := normalizeMessageID(match)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func chooseThreadKey(messageID string, references []string, folderID string, uidValidity, uid uint32) string {
	if len(references) > 0 {
		return references[0]
	}
	if value := normalizeMessageID(messageID); value != "" {
		return value
	}
	return fmt.Sprintf("uid:%s:%d:%d", folderID, uidValidity, uid)
}

func normalizeMessageID(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "<>"))
}

func flags(values []string) (bool, bool) {
	var seen, starred bool
	for _, value := range values {
		switch strings.ToLower(value) {
		case `\seen`:
			seen = true
		case `\flagged`:
			starred = true
		}
	}
	return seen, starred
}

func canonicalFlags(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	return result
}

func addressesJSON(groups []connectors.RemoteAddress) string {
	values := make([]map[string]string, 0, len(groups))
	for _, address := range groups {
		values = append(values, map[string]string{"name": address.Name, "email": address.Email})
	}
	return marshal(values)
}

func addressText(groups ...[]connectors.RemoteAddress) string {
	var values []string
	for _, group := range groups {
		for _, address := range group {
			values = append(values, address.Name, address.Email)
		}
	}
	return strings.Join(values, " ")
}

func compactPreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:240])
	}
	return value
}

func partPath(path []int) string {
	values := make([]string, 0, len(path))
	for _, value := range path {
		values = append(values, fmt.Sprintf("%d", value))
	}
	return strings.Join(values, ".")
}

func firstReference(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return normalizeMessageID(values[0])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func marshal(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixMilli()
}

func nullableDelimiter(value rune) any {
	if value == 0 {
		return nil
	}
	return string(value)
}

func mustID() string {
	id, err := store.NewID()
	if err != nil {
		panic(err)
	}
	return id
}

var _ mailSync.IngestSink = (*Store)(nil)
