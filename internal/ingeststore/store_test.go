package ingeststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/repository"
	"mailmanager/internal/search"
	baseStore "mailmanager/internal/store"
	mailSync "mailmanager/internal/sync"
)

func TestStoreMessagesMakesConversationSearchable(t *testing.T) {
	ctx := context.Background()
	database, err := baseStore.Open(ctx, filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	account, err := repo.CreateAccount(ctx, repository.AccountInput{
		DisplayName: "Work", Email: "work@example.com", Provider: "imap", Color: "#315B7D",
		AuthType: "password", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: []byte("encrypted"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ingest := New(database)
	folders, err := ingest.UpsertFolders(ctx, account.ID, []connectors.RemoteMailbox{{
		Name: "INBOX", Delimiter: '/', Selectable: true, Role: accounts.FolderInbox,
	}})
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	batch := mailSync.MessageBatch{AccountID: account.ID, FolderID: folders[0].ID, UIDValidity: 42, Messages: []connectors.RemoteMessage{{
		UID: 7, Flags: []string{}, InternalDate: received, RFC822Size: 1024,
		Envelope: connectors.RemoteEnvelope{
			Subject: "项目进度", MessageID: "<message@example.com>", Date: received,
			From: []connectors.RemoteAddress{{Name: "张三", Email: "sender@example.com"}},
			To:   []connectors.RemoteAddress{{Email: "work@example.com"}},
		},
		TextBody: "你好，新的里程碑已经完成。",
		HTMLBody: "<p>你好，新的里程碑已经完成。</p>",
		Parts:    []connectors.RemotePart{{Path: []int{2}, MediaType: "application/pdf", Disposition: "attachment", Filename: "report.pdf", Size: 123}},
	}}}
	if err := ingest.StoreMessages(ctx, batch); err != nil {
		t.Fatal(err)
	}
	items, _, err := repo.ListConversations(ctx, repository.ConversationQuery{
		InboxOnly: true, SearchExpression: search.MatchQuery("里程碑"), Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Subject != "项目进度" || !items[0].HasAttachments || items[0].UnreadCount != 1 {
		t.Fatalf("unexpected conversations: %#v", items)
	}
	detail, err := repo.GetConversation(ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].BodyText == "" || len(detail.Messages[0].Attachments) != 1 {
		t.Fatalf("unexpected detail: %#v", detail)
	}
}

func TestStoreMessagesDeduplicatesGmailFolderCopies(t *testing.T) {
	ctx := context.Background()
	database, err := baseStore.Open(ctx, filepath.Join(t.TempDir(), "gmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database)
	account, err := repo.CreateAccount(ctx, repository.AccountInput{
		DisplayName: "Gmail", Email: "owner@gmail.com", Provider: string(accounts.ProviderGoogle), Color: "#315B7D",
		AuthType: "password", IMAPHost: "imap.gmail.com", IMAPPort: 993,
		SMTPHost: "smtp.gmail.com", SMTPPort: 465, CredentialEncrypted: []byte("encrypted"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ingest := New(database)
	folders, err := ingest.UpsertFolders(ctx, account.ID, []connectors.RemoteMailbox{
		{Name: "INBOX", Selectable: true, Role: accounts.FolderInbox},
		{Name: "[Gmail]/All Mail", Selectable: true, Role: accounts.FolderAll},
	})
	if err != nil {
		t.Fatal(err)
	}

	received := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	message := connectors.RemoteMessage{
		InternalDate: received, RFC822Size: 512,
		Envelope: connectors.RemoteEnvelope{
			Subject: "stable", MessageID: "<stable@gmail.com>", Date: received,
			From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
			To:   []connectors.RemoteAddress{{Email: "owner@gmail.com"}},
		},
		TextBody: "same Gmail message",
	}
	if err := ingest.StoreMessages(ctx, mailSync.MessageBatch{
		AccountID: account.ID, FolderID: folders[0].ID, UIDValidity: 1,
		Provider: accounts.ProviderGoogle, Messages: []connectors.RemoteMessage{{UID: 1}},
	}); err == nil {
		t.Fatal("Google message without X-GM-MSGID was accepted")
	}

	const gmailMessageID = ^uint64(0)
	for index, folder := range folders {
		copy := message
		copy.UID = uint32(index + 10)
		copy.GmailMessageID = gmailMessageID
		if index == 1 {
			copy.TextBody = "duplicate folder copy must not replace canonical content"
		}
		if err := ingest.StoreMessages(ctx, mailSync.MessageBatch{
			AccountID: account.ID, FolderID: folder.ID, UIDValidity: uint32(index + 1),
			Provider: accounts.ProviderGoogle, Messages: []connectors.RemoteMessage{copy},
		}); err != nil {
			t.Fatal(err)
		}
	}

	assertCount(t, database.DB(), `SELECT COUNT(*) FROM messages WHERE account_id = ?`, 1, account.ID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_locations WHERE account_id = ?`, 2, account.ID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM conversations WHERE account_id = ?`, 1, account.ID)
	var storedGmailID, storedBody string
	if err := database.DB().QueryRowContext(ctx, `SELECT gmail_message_id, body_text FROM messages WHERE account_id = ?`, account.ID).Scan(&storedGmailID, &storedBody); err != nil {
		t.Fatal(err)
	}
	if want := strconv.FormatUint(gmailMessageID, 10); storedGmailID != want {
		t.Fatalf("gmail_message_id = %q, want %q", storedGmailID, want)
	}
	if storedBody != message.TextBody {
		t.Fatalf("canonical Gmail body was replaced by folder copy: %q", storedBody)
	}
}

func TestReconcileFolderUpdatesFlagsAndDeletesExpungedOrphans(t *testing.T) {
	ctx := context.Background()
	database, repo, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()

	received := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	for _, uid := range []uint32{5, 6} {
		message := connectors.RemoteMessage{
			UID: uid, InternalDate: received.Add(time.Duration(uid) * time.Minute), RFC822Size: 100,
			Envelope: connectors.RemoteEnvelope{
				Subject: "state", MessageID: "<state-" + string(rune('0'+uid)) + "@example.com>",
				Date: received, From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
				To: []connectors.RemoteAddress{{Email: "work@example.com"}},
			},
			TextBody: "searchable state body",
		}
		if err := ingest.StoreMessages(ctx, mailSync.MessageBatch{
			AccountID: accountID, FolderID: folderID, UIDValidity: 42,
			Messages: []connectors.RemoteMessage{message},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := ingest.ReconcileFolder(ctx, mailSync.FolderSnapshot{
		AccountID: accountID, FolderID: folderID, UIDValidity: 42,
		RemoteUIDs: []uint32{5},
		States:     []connectors.RemoteMessageState{{UID: 5, Flags: []string{`\Seen`}, ModSeq: 9}},
	}); err != nil {
		t.Fatal(err)
	}

	var seen int
	var flagsJSON string
	if err := database.DB().QueryRowContext(ctx, `
		SELECT m.seen, ml.flags_json FROM messages m
		JOIN message_locations ml ON ml.message_id = m.id
		WHERE ml.folder_id = ? AND ml.uid = 5`, folderID).Scan(&seen, &flagsJSON); err != nil {
		t.Fatal(err)
	}
	var flags []string
	if err := json.Unmarshal([]byte(flagsJSON), &flags); err != nil {
		t.Fatal(err)
	}
	if seen != 1 || len(flags) != 1 || flags[0] != `\Seen` {
		t.Fatalf("UID 5 state = seen:%d flags:%v", seen, flags)
	}

	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_locations WHERE folder_id = ? AND uid = 6`, 0, folderID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM messages WHERE rfc_message_id = '<state-6@example.com>'`, 0)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_search WHERE body_tokens MATCH 'searchable'`, 1)

	items, _, err := repo.ListConversations(ctx, repository.ConversationQuery{InboxOnly: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UnreadCount != 0 {
		t.Fatalf("unexpected conversations after reconciliation: %#v", items)
	}
}

func TestReconcileFolderRejectsIncompleteSnapshotWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	database, _, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()

	message := connectors.RemoteMessage{
		UID: 5, InternalDate: time.Now().UTC(), RFC822Size: 100,
		Envelope: connectors.RemoteEnvelope{
			Subject: "state", MessageID: "<incomplete-state@example.com>", Date: time.Now().UTC(),
			From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
			To:   []connectors.RemoteAddress{{Email: "work@example.com"}},
		},
		TextBody: "keep this message",
	}
	if err := ingest.StoreMessages(ctx, mailSync.MessageBatch{
		AccountID: accountID, FolderID: folderID, UIDValidity: 42,
		Messages: []connectors.RemoteMessage{message},
	}); err != nil {
		t.Fatal(err)
	}
	err := ingest.ReconcileFolder(ctx, mailSync.FolderSnapshot{
		AccountID: accountID, FolderID: folderID, UIDValidity: 42,
		RemoteUIDs: []uint32{5},
	})
	if err == nil {
		t.Fatal("incomplete folder snapshot was accepted")
	}
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_locations WHERE folder_id = ? AND uid = 5`, 1, folderID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM messages WHERE rfc_message_id = '<incomplete-state@example.com>'`, 1)
}

func TestUIDValidityRebuildDeletesOrphanMessagesAndSearchRows(t *testing.T) {
	ctx := context.Background()
	database, _, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()

	message := connectors.RemoteMessage{
		UID: 7, InternalDate: time.Now().UTC(), RFC822Size: 100,
		Envelope: connectors.RemoteEnvelope{
			Subject: "old validity", MessageID: "<old-validity@example.com>", Date: time.Now().UTC(),
			From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
			To:   []connectors.RemoteAddress{{Email: "work@example.com"}},
		},
		TextBody: "orphan cleanup token",
	}
	if err := ingest.StoreMessages(ctx, mailSync.MessageBatch{
		AccountID: accountID, FolderID: folderID, UIDValidity: 42,
		Messages: []connectors.RemoteMessage{message},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ingest.PrepareFolder(ctx, mailSync.ReconcileDecision{
		Action:     mailSync.ReconcileRebuild,
		Checkpoint: mailSync.Checkpoint{AccountID: accountID, FolderID: folderID, UIDValidity: 43, UpdatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_locations WHERE folder_id = ?`, 0, folderID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM messages WHERE account_id = ?`, 0, accountID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_search WHERE account_id = ?`, 0, accountID)
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM conversations WHERE account_id = ?`, 0, accountID)
}

func TestUpsertFoldersPreservesUserRoleConfirmation(t *testing.T) {
	ctx := context.Background()
	database, repo, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()
	if _, err := repo.UpdateFolderRole(ctx, folderID, "sent"); err != nil {
		t.Fatal(err)
	}

	folders, err := ingest.UpsertFolders(ctx, accountID, []connectors.RemoteMailbox{{
		Name: "INBOX", Delimiter: '/', Selectable: true,
		Role: accounts.FolderInbox, RoleSource: accounts.RoleFromSpecialUse,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Role != "sent" || folders[0].RoleSource != "user" {
		t.Fatalf("folder discovery overwrote user role: %+v", folders)
	}
}

func TestCheckpointBodyRefreshClearsOnlyOnCompletion(t *testing.T) {
	ctx := context.Background()
	database, _, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()
	if _, err := database.DB().ExecContext(ctx, `
		UPDATE sync_checkpoints SET body_refresh_required = 1 WHERE account_id = ? AND folder_id = ?`, accountID, folderID); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := ingest.Checkpoint(ctx, accountID, folderID)
	if err != nil {
		t.Fatal(err)
	}
	if !checkpoint.BodyRefreshRequired {
		t.Fatal("body refresh flag was not loaded")
	}
	checkpoint.UIDValidity = 42
	checkpoint.UIDNext = 8
	checkpoint.LastUID = 7
	checkpoint.BodyRefreshRequired = false
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := ingest.CompleteFolder(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	completed, err := ingest.Checkpoint(ctx, accountID, folderID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.BodyRefreshRequired {
		t.Fatal("body refresh flag remained set after successful completion")
	}
}

func TestBodyRefreshUpdatesExistingMessageAndPreservesAttachment(t *testing.T) {
	ctx := context.Background()
	database, _, ingest, accountID, folderID := newIngestFixture(t)
	defer database.Close()
	received := time.Now().UTC()
	message := connectors.RemoteMessage{
		UID: 7, InternalDate: received, RFC822Size: 256,
		Envelope: connectors.RemoteEnvelope{
			Subject: "Styled", MessageID: "<styled@example.com>", Date: received,
			From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
			To:   []connectors.RemoteAddress{{Email: "work@example.com"}},
		},
		TextBody: "old body", HTMLBody: "<p>old body</p>",
		Parts: []connectors.RemotePart{{Path: []int{2}, MediaType: "application/pdf", Disposition: "attachment", Filename: "report.pdf", Size: 42}},
	}
	batch := mailSync.MessageBatch{AccountID: accountID, FolderID: folderID, UIDValidity: 42, Messages: []connectors.RemoteMessage{message}}
	if err := ingest.StoreMessages(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var messageID, attachmentID string
	if err := database.DB().QueryRowContext(ctx, `SELECT id FROM messages WHERE account_id = ?`, accountID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRowContext(ctx, `SELECT id FROM attachments WHERE message_id = ?`, messageID).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	message.TextBody = "new searchable body"
	message.HTMLBody = `<style>.hero{color:red}</style><p class="hero">new body</p>`
	batch.Messages = []connectors.RemoteMessage{message}
	batch.RefreshBody = true
	if err := ingest.StoreMessages(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var storedID, storedHTML, storedAttachmentID string
	if err := database.DB().QueryRowContext(ctx, `SELECT id, body_html_clean FROM messages WHERE account_id = ?`, accountID).Scan(&storedID, &storedHTML); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRowContext(ctx, `SELECT id FROM attachments WHERE message_id = ?`, messageID).Scan(&storedAttachmentID); err != nil {
		t.Fatal(err)
	}
	if storedID != messageID || storedAttachmentID != attachmentID || !strings.Contains(storedHTML, "<style>") {
		t.Fatalf("refresh changed stable records or lost CSS: message=%q attachment=%q html=%q", storedID, storedAttachmentID, storedHTML)
	}
	assertCount(t, database.DB(), `SELECT COUNT(*) FROM message_search WHERE message_id = ? AND body_tokens MATCH 'searchable'`, 1, messageID)
}

func newIngestFixture(t *testing.T) (*baseStore.Store, *repository.Repository, *Store, string, string) {
	t.Helper()
	ctx := context.Background()
	database, err := baseStore.Open(ctx, filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		database.Close()
		t.Fatal(err)
	}
	repo := repository.New(database)
	account, err := repo.CreateAccount(ctx, repository.AccountInput{
		DisplayName: "Work", Email: "work@example.com", Provider: "imap", Color: "#315B7D",
		AuthType: "password", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: []byte("encrypted"),
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	ingest := New(database)
	folders, err := ingest.UpsertFolders(ctx, account.ID, []connectors.RemoteMailbox{{
		Name: "INBOX", Delimiter: '/', Selectable: true, Role: accounts.FolderInbox,
	}})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database, repo, ingest, account.ID, folders[0].ID
}

func assertCount(t *testing.T, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count for %q = %d, want %d", query, got, want)
	}
}
