package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mailmanager/internal/search"
	"mailmanager/internal/store"
)

func TestAccountCreationAndListing(t *testing.T) {
	ctx, repository, db := newTestRepository(t)

	created, err := repository.CreateAccount(ctx, AccountInput{
		DisplayName: "  Personal  ", Email: "USER@Example.com", Provider: "IMAP", Color: "#1a2b3c",
		AuthType: "PASSWORD", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: []byte("encrypted"),
		SignatureHTML: "<p>Regards</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.DisplayName != "Personal" || created.Email != "user@example.com" || created.Provider != "imap" || created.Color != "#1A2B3C" {
		t.Fatalf("account was not normalized: %+v", created)
	}

	var createdAt int64
	if err := db.QueryRowContext(ctx, `SELECT created_at FROM accounts WHERE id = ?`, created.ID).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	if createdAt < 1_000_000_000_000 {
		t.Fatalf("account timestamp is not milliseconds: %d", createdAt)
	}
	var identityID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM identities WHERE account_id = ? AND is_primary = 1`, created.ID).Scan(&identityID); err != nil {
		t.Fatal(err)
	}

	folderID := "folder-account-list"
	lastSynced := time.Date(2026, time.July, 11, 12, 13, 14, 321_000_000, time.UTC)
	execSQL(t, db, `
		INSERT INTO folders (id, account_id, remote_name, display_name, role, created_at, updated_at)
		VALUES (?, ?, 'INBOX', 'Inbox', 'inbox', ?, ?)`, folderID, created.ID, createdAt, createdAt)
	execSQL(t, db, `
		INSERT INTO sync_checkpoints (id, account_id, folder_id, backfill_before, state, last_success_at, updated_at)
		VALUES ('checkpoint-account-list', ?, ?, NULL, 'idle', ?, ?)`, created.ID, folderID, lastSynced.UnixMilli(), createdAt)

	accounts, err := repository.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].LastSyncedAt == nil || !accounts[0].LastSyncedAt.Equal(lastSynced) || accounts[0].BackfillProgress != 100 {
		t.Fatalf("unexpected account listing: %+v", accounts)
	}
	record, err := repository.GetAccount(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.CredentialEncrypted) != "encrypted" || record.IMAPPort != 993 || record.SMTPPort != 465 {
		t.Fatalf("unexpected account record: %+v", record)
	}

	_, err = repository.CreateAccount(ctx, AccountInput{
		DisplayName: "Duplicate", Email: "user@example.com", Provider: "imap", Color: "#112233",
		AuthType: "password", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: []byte("encrypted"),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate account error = %v, want conflict", err)
	}
}

func TestAccountConnectionUpdateAndOAuthTokenReplacement(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	password, err := repository.CreateAccount(ctx, AccountInput{
		DisplayName: "Password", Email: "password@example.com", Provider: "imap", Color: "#112233",
		AuthType: "password", IMAPHost: "imap.old.example", IMAPPort: 993,
		SMTPHost: "smtp.old.example", SMTPPort: 465, CredentialEncrypted: []byte("old-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateAccountStatus(ctx, password.ID, "ready", ""); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdatePasswordAccount(ctx, password.ID, PasswordAccountUpdate{
		DisplayName: "Updated Password", Color: "#AABBCC", SignatureHTML: "<p>Updated</p>",
		IMAPHost: "imap.new.example", IMAPPort: 143, SMTPHost: "smtp.new.example", SMTPPort: 587,
		CredentialEncrypted: []byte("new-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != password.ID || updated.Status != "pending" || updated.DisplayName != "Updated Password" {
		t.Fatalf("unexpected password update: %+v", updated)
	}
	record, err := repository.GetAccount(ctx, password.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.IMAPHost != "imap.new.example" || record.IMAPPort != 143 || record.SMTPHost != "smtp.new.example" || record.SMTPPort != 587 || string(record.CredentialEncrypted) != "new-secret" {
		t.Fatalf("connection update was not persisted: %+v", record)
	}
	var signature string
	if err := db.QueryRowContext(ctx, `SELECT signature_html FROM identities WHERE account_id = ? AND is_primary = 1`, password.ID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if signature != "<p>Updated</p>" {
		t.Fatalf("signature = %q", signature)
	}

	oauthAccount, err := repository.CreateAccount(ctx, AccountInput{
		DisplayName: "OAuth", Email: "oauth@example.com", Provider: "google", Color: "#334455",
		AuthType: "oauth2", OAuthTokenEncrypted: []byte("old-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateAccountStatus(ctx, oauthAccount.ID, "reauth_required", "oauth_refresh_failed"); err != nil {
		t.Fatal(err)
	}
	replaced, err := repository.ReplaceOAuthToken(ctx, oauthAccount.ID, "google", "oauth@example.com", []byte("new-token"))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ID != oauthAccount.ID || replaced.Status != "pending" {
		t.Fatalf("OAuth account was not updated in place: %+v", replaced)
	}
	record, err = repository.GetAccount(ctx, oauthAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.OAuthTokenEncrypted) != "new-token" {
		t.Fatalf("OAuth token = %q", record.OAuthTokenEncrypted)
	}
	if _, err := repository.ReplaceOAuthToken(ctx, oauthAccount.ID, "microsoft", "oauth@example.com", []byte("wrong")); !errors.Is(err, ErrConflict) {
		t.Fatalf("provider mismatch error = %v", err)
	}
}

func TestFoldersMessagesConversationCursorAndSearch(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	accountID, _ := createTestAccount(t, ctx, repository, "mail@example.com")
	base := time.Date(2026, time.July, 11, 8, 0, 0, 100_000_000, time.UTC)
	inboxID := "folder-inbox"
	execSQL(t, db, `
		INSERT INTO folders (id, account_id, remote_name, display_name, role, created_at, updated_at)
		VALUES (?, ?, 'INBOX', 'Inbox', 'inbox', ?, ?)`, inboxID, accountID, base.UnixMilli(), base.UnixMilli())

	seedConversation(t, db, accountID, inboxID, "conversation-1", "message-1", "First", "first preview", base.Add(time.Millisecond), false, `null`)
	seedConversation(t, db, accountID, inboxID, "conversation-2", "message-2", "Project update", "project preview", base.Add(2*time.Millisecond), true, `[{"name":"Li","email":"li@example.com"}]`)
	seedConversation(t, db, accountID, inboxID, "conversation-3", "message-3", "Newest", "newest preview", base.Add(3*time.Millisecond), false, `[{"email":"new@example.com"}]`)
	execSQL(t, db, `
		INSERT INTO attachments (id, message_id, part_id, filename, content_type, disposition, size_bytes, created_at)
		VALUES ('attachment-2', 'message-2', '2', 'report.pdf', 'application/pdf', 'attachment', 42, ?)`, base.UnixMilli())
	execSQL(t, db, `
		INSERT INTO message_search (message_id, account_id, subject_tokens, address_tokens, body_tokens)
		VALUES ('message-2', ?, ?, ?, ?)`, accountID, search.IndexText("项目 Project update"), search.IndexText("li@example.com"), search.IndexText("本周项目进展"))

	mailboxes, err := repository.ListMailboxes(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 1 || mailboxes[0].UnreadCount != 2 {
		t.Fatalf("unexpected mailbox counts: %+v", mailboxes)
	}

	firstPage, next, err := repository.ListConversations(ctx, ConversationQuery{AccountID: accountID, InboxOnly: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != "conversation-3" || firstPage[1].ID != "conversation-2" || next == nil {
		t.Fatalf("unexpected first page: items=%+v next=%+v", firstPage, next)
	}
	if !firstPage[0].LatestAt.Equal(base.Add(3 * time.Millisecond)) {
		t.Fatalf("latest timestamp lost millisecond precision: %v", firstPage[0].LatestAt)
	}
	if len(firstPage[1].Participants) != 1 || !firstPage[1].HasAttachments {
		t.Fatalf("conversation JSON or attachment summary is wrong: %+v", firstPage[1])
	}

	secondPage, finalCursor, err := repository.ListConversations(ctx, ConversationQuery{
		AccountID: accountID, InboxOnly: true, Before: next.Timestamp, BeforeID: next.ID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != "conversation-1" || finalCursor != nil {
		t.Fatalf("unexpected second page: items=%+v next=%+v", secondPage, finalCursor)
	}
	if secondPage[0].Participants == nil || len(secondPage[0].Participants) != 0 {
		t.Fatalf("null participants must normalize to an empty array: %#v", secondPage[0].Participants)
	}

	searchResults, _, err := repository.ListConversations(ctx, ConversationQuery{
		AccountID: accountID, SearchExpression: search.MatchQuery("项目"), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResults) != 1 || searchResults[0].ID != "conversation-2" {
		t.Fatalf("unexpected FTS results: %+v", searchResults)
	}

	detail, err := repository.GetConversation(ctx, "conversation-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages) != 1 || !detail.Messages[0].ReceivedAt.Equal(base.Add(2*time.Millisecond)) || len(detail.Messages[0].Attachments) != 1 {
		t.Fatalf("unexpected conversation detail: %+v", detail)
	}
	_, _, err = repository.ListConversations(ctx, ConversationQuery{Before: next.Timestamp, Limit: 10})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial cursor error = %v, want invalid", err)
	}
}

func TestGlobalMailboxesIncludeVirtualRoles(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	accountID, _ := createTestAccount(t, ctx, repository, "global@example.com")
	base := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	inboxID := "global-inbox"
	allID := "global-all"
	seedFolder(t, db, inboxID, accountID, "INBOX", "inbox", base)
	seedFolder(t, db, allID, accountID, "All Mail", "all", base)
	seedConversation(t, db, accountID, inboxID, "global-conversation-inbox", "global-message-inbox", "Inbox", "preview", base, false, `[]`)
	seedConversation(t, db, accountID, allID, "global-conversation-archive", "global-message-archive", "Archive", "preview", base.Add(time.Second), false, `[]`)
	execSQL(t, db, `UPDATE messages SET flagged = 1 WHERE id = 'global-message-inbox'`)

	mailboxes, err := repository.ListMailboxes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	roles := make(map[string]MailboxSummary)
	for _, mailbox := range mailboxes {
		if mailbox.AccountID == "" {
			roles[mailbox.Role] = mailbox
		}
	}
	for _, role := range []string{"inbox", "starred", "sent", "drafts", "archive", "trash"} {
		if _, ok := roles[role]; !ok {
			t.Fatalf("global mailbox role %q missing from %+v", role, mailboxes)
		}
	}
	if roles["inbox"].UnreadCount != 1 || roles["starred"].UnreadCount != 1 || roles["archive"].UnreadCount != 1 {
		t.Fatalf("unexpected global unread counts: %+v", roles)
	}
	updatedFolder, err := repository.UpdateFolderRole(ctx, allID, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if updatedFolder.Role != "archive" || updatedFolder.RoleSource != "user" {
		t.Fatalf("user folder role was not persisted: %+v", updatedFolder)
	}

	archived, _, err := repository.ListConversations(ctx, ConversationQuery{MailboxRole: "archive", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].ID != "global-conversation-archive" {
		t.Fatalf("unexpected global archive conversations: %+v", archived)
	}
}

func TestDraftOptimisticConflictAndQueueDeduplication(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	accountID, identityID := createTestAccount(t, ctx, repository, "draft@example.com")
	input := DraftInput{
		AccountID: accountID, IdentityID: identityID,
		To:      []Address{{Name: "Recipient", Email: "recipient@example.com"}},
		Subject: " First draft ", BodyText: "hello", BodyHTML: "<p>hello</p>",
	}
	created, err := repository.CreateDraft(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.Subject != "First draft" || created.CC == nil || created.BCC == nil || created.Attachments == nil {
		t.Fatalf("unexpected created draft: %+v", created)
	}
	loaded, err := repository.GetDraft(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.CreatedAt.Equal(created.CreatedAt) || loaded.To == nil || loaded.CC == nil || loaded.BCC == nil {
		t.Fatalf("draft did not round-trip cleanly: created=%+v loaded=%+v", created, loaded)
	}
	var recipientsJSON string
	if err := db.QueryRowContext(ctx, `SELECT cc_json FROM drafts WHERE id = ?`, created.ID).Scan(&recipientsJSON); err != nil {
		t.Fatal(err)
	}
	if recipientsJSON != "[]" {
		t.Fatalf("empty address list stored as %q, want []", recipientsJSON)
	}
	attachment, err := repository.AddDraftAttachment(ctx, created.ID, "brief.pdf", "application/pdf", "C:/mailmanager/brief.pdf", 42, []byte("digest"))
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := repository.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || len(drafts[0].Attachments) != 1 || drafts[0].Attachments[0].ID != attachment.ID || drafts[0].State != "draft" {
		t.Fatalf("draft list did not include durable attachments and state: %+v", drafts)
	}

	input.Version = created.Version
	input.Subject = "Updated"
	updated, err := repository.UpdateDraft(ctx, created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Subject != "Updated" || !updated.UpdatedAt.Equal(fromUnixMillis(updated.UpdatedAt.UnixMilli())) {
		t.Fatalf("unexpected updated draft: %+v", updated)
	}
	input.Subject = "Stale"
	if _, err := repository.UpdateDraft(ctx, created.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}

	const queueWorkers = 8
	outboxIDs := make([]string, queueWorkers)
	queueErrors := make([]error, queueWorkers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := range queueWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			outboxIDs[index], queueErrors[index] = repository.QueueDraft(ctx, created.ID)
		}()
	}
	close(start)
	workers.Wait()
	firstOutboxID := outboxIDs[0]
	for index := range queueWorkers {
		if queueErrors[index] != nil {
			t.Fatalf("queue worker %d: %v", index, queueErrors[index])
		}
		if outboxIDs[index] != firstOutboxID {
			t.Fatalf("queue worker %d returned %q, want %q", index, outboxIDs[index], firstOutboxID)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE draft_id = ?`, created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox count = %d, err = %v", count, err)
	}
	var nextAttemptAt int64
	if err := db.QueryRowContext(ctx, `SELECT next_attempt_at FROM outbox WHERE id = ?`, firstOutboxID).Scan(&nextAttemptAt); err != nil {
		t.Fatal(err)
	}
	if nextAttemptAt < 1_000_000_000_000 {
		t.Fatalf("outbox timestamp is not milliseconds: %d", nextAttemptAt)
	}
	status, err := repository.GetOutboxStatus(ctx, firstOutboxID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ID != firstOutboxID || status.DraftID != created.ID || status.Status != "queued" || status.UpdatedAt.IsZero() {
		t.Fatalf("unexpected outbox status: %+v", status)
	}
}

func TestOperationValidationAndUndo(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	accountID, _ := createTestAccount(t, ctx, repository, "operations@example.com")
	otherAccountID, _ := createTestAccount(t, ctx, repository, "other@example.com")
	base := time.Date(2026, time.July, 11, 9, 0, 0, 0, time.UTC)
	inboxID := "operation-inbox"
	trashID := "operation-trash"
	otherFolderID := "other-inbox"
	seedFolder(t, db, inboxID, accountID, "INBOX", "inbox", base)
	seedFolder(t, db, trashID, accountID, "Trash", "trash", base)
	seedFolder(t, db, otherFolderID, otherAccountID, "INBOX", "inbox", base)
	seedConversation(t, db, accountID, inboxID, "operation-conversation", "operation-message", "Operation", "preview", base, false, `[]`)

	invalidCases := []struct {
		name          string
		accountID     string
		kind          string
		targets       []string
		destinationID string
	}{
		{name: "unknown kind", accountID: accountID, kind: "label", targets: []string{"operation-message"}},
		{name: "duplicate target", accountID: accountID, kind: "archive", targets: []string{"operation-message", "operation-message"}},
		{name: "move without destination", accountID: accountID, kind: "move", targets: []string{"operation-message"}},
		{name: "destination on non-move", accountID: accountID, kind: "archive", targets: []string{"operation-message"}, destinationID: trashID},
		{name: "destination from another account", accountID: accountID, kind: "move", targets: []string{"operation-message"}, destinationID: otherFolderID},
		{name: "target from another account", accountID: otherAccountID, kind: "archive", targets: []string{"operation-message"}},
		{name: "delete outside trash", accountID: accountID, kind: "delete", targets: []string{"operation-message"}},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.CreateOperation(ctx, test.accountID, test.kind, test.targets, test.destinationID)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}

	operation, err := repository.CreateOperation(ctx, accountID, "archive", []string{"operation-message"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if operation.UndoUntil == nil || operation.UndoUntil.Sub(operation.CreatedAt) != 5*time.Second {
		t.Fatalf("unexpected undo window: %+v", operation)
	}
	var executeAfter int64
	if err := db.QueryRowContext(ctx, `SELECT execute_after FROM operations WHERE id = ?`, operation.ID).Scan(&executeAfter); err != nil {
		t.Fatal(err)
	}
	if executeAfter != operation.UndoUntil.UnixMilli() {
		t.Fatalf("stored execute_at = %d, want %d", executeAfter, operation.UndoUntil.UnixMilli())
	}

	undone, err := repository.UndoOperation(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if undone.Status != "cancelled" || len(undone.TargetMessageIDs) != 1 || undone.TargetMessageIDs[0] != "operation-message" || !undone.CreatedAt.Equal(operation.CreatedAt) {
		t.Fatalf("unexpected undone operation: %+v", undone)
	}
	if _, err := repository.UndoOperation(ctx, operation.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second undo error = %v, want conflict", err)
	}
	malformed, err := repository.CreateOperation(ctx, accountID, "trash", []string{"operation-message"}, "")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, `UPDATE operations SET payload_json = '{' WHERE id = ?`, malformed.ID)
	if _, err := repository.UndoOperation(ctx, malformed.ID); err == nil {
		t.Fatal("malformed operation payload was accepted")
	}
	var malformedStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM operations WHERE id = ?`, malformed.ID).Scan(&malformedStatus); err != nil {
		t.Fatal(err)
	}
	if malformedStatus != "queued" {
		t.Fatalf("malformed operation was mutated to %q", malformedStatus)
	}

	execSQL(t, db, `
		INSERT INTO message_locations (id, message_id, account_id, folder_id, uid_validity, uid, created_at, updated_at)
		VALUES ('operation-trash-location', 'operation-message', ?, ?, 1, 99, ?, ?)`, accountID, trashID, base.UnixMilli(), base.UnixMilli())
	if _, err := repository.CreateOperation(ctx, accountID, "delete", []string{"operation-message"}, ""); err != nil {
		t.Fatalf("delete from trash: %v", err)
	}
}

func TestSystemStatus(t *testing.T) {
	ctx, repository, db := newTestRepository(t)
	status, err := repository.SystemStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.AccountCount != 0 || status.MessageCount != 0 || status.PendingActions != 0 || status.ServerTime.Location() != time.UTC {
		t.Fatalf("unexpected empty status: %+v", status)
	}

	accountID, _ := createTestAccount(t, ctx, repository, "status@example.com")
	base := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	seedFolder(t, db, "status-inbox", accountID, "INBOX", "inbox", base)
	seedConversation(t, db, accountID, "status-inbox", "status-conversation", "status-message", "Status", "preview", base, false, `[]`)
	if _, err := repository.CreateOperation(ctx, accountID, "archive", []string{"status-message"}, ""); err != nil {
		t.Fatal(err)
	}
	status, err = repository.SystemStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.AccountCount != 1 || status.MessageCount != 1 || status.PendingActions != 1 {
		t.Fatalf("unexpected populated status: %+v", status)
	}
}

func TestRepositoryWritesUseStoreCoordinator(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenWithOptions(ctx, filepath.Join(t.TempDir(), "mailmanager.db"), store.Options{
		BusyTimeout: 25 * time.Millisecond, MaxOpenConns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repository := New(database)
	account, err := repository.CreateAccount(ctx, AccountInput{
		DisplayName: "Owner", Email: "owner@example.com", Provider: "imap", Color: "#336699",
		AuthType: "password", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: []byte("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- database.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE accounts SET updated_at = updated_at + 1 WHERE id = ?`, account.ID); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondDone := make(chan error, 1)
	go func() { secondDone <- repository.UpdateAccountStatus(ctx, account.ID, "ready", "") }()
	select {
	case err := <-secondDone:
		close(release)
		<-firstDone
		t.Fatalf("repository write bypassed store coordinator: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func newTestRepository(t *testing.T) (context.Context, *Repository, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "mailmanager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, New(database), database.DB()
}

func createTestAccount(t *testing.T, ctx context.Context, repository *Repository, email string) (string, string) {
	t.Helper()
	account, err := repository.CreateAccount(ctx, AccountInput{
		DisplayName: email, Email: email, Provider: "imap", Color: "#123456", AuthType: "password",
		IMAPHost: "imap.example.com", IMAPPort: 993, SMTPHost: "smtp.example.com", SMTPPort: 465,
		CredentialEncrypted: []byte("encrypted"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var identityID string
	if err := repository.db.QueryRowContext(ctx, `SELECT id FROM identities WHERE account_id = ?`, account.ID).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	return account.ID, identityID
}

func seedFolder(t *testing.T, db *sql.DB, folderID, accountID, remoteName, role string, at time.Time) {
	t.Helper()
	execSQL(t, db, `
		INSERT INTO folders (id, account_id, remote_name, display_name, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, folderID, accountID, remoteName, remoteName, role, at.UnixMilli(), at.UnixMilli())
}

func seedConversation(t *testing.T, db *sql.DB, accountID, folderID, conversationID, messageID, subject, preview string, at time.Time, seen bool, fromJSON string) {
	t.Helper()
	seenValue := 0
	if seen {
		seenValue = 1
	}
	unreadCount := 1 - seenValue
	execSQL(t, db, `
		INSERT INTO conversations (
			id, account_id, thread_key, subject, preview, latest_at, message_count, unread_count, starred, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?)`,
		conversationID, accountID, "thread-"+conversationID, subject, preview, at.UnixMilli(), unreadCount, at.UnixMilli(), at.UnixMilli())
	execSQL(t, db, `
		INSERT INTO messages (
			id, account_id, conversation_id, subject, from_json, to_json, cc_json, bcc_json, reply_to_json,
			sent_at, received_at, preview, body_text, body_html_clean, seen, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, '[]', '[]', '[]', '[]', ?, ?, ?, ?, ?, ?, ?, ?)`,
		messageID, accountID, conversationID, subject, fromJSON, at.UnixMilli(), at.UnixMilli(), preview,
		"Plain body for "+subject, "<p>Body</p>", seenValue, at.UnixMilli(), at.UnixMilli())
	execSQL(t, db, `
		INSERT INTO message_locations (id, message_id, account_id, folder_id, uid_validity, uid, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		"location-"+messageID, messageID, accountID, folderID, at.UnixMilli(), at.UnixMilli(), at.UnixMilli())
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("execute SQL: %v\n%s", err, query)
	}
}

func TestAddressJSONIsValid(t *testing.T) {
	var addresses []Address
	decodeAddresses(`null`, &addresses)
	encoded, err := json.Marshal(addresses)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("normalized addresses = %s, %v", encoded, err)
	}
}
