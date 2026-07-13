package store

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	appMigrations "mailmanager/migrations"
)

func TestNewIDIsUUIDv7(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	compact := strings.ReplaceAll(id, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("invalid UUID %q: %v", id, err)
	}
	if decoded[6]>>4 != 7 || decoded[8]>>6 != 2 {
		t.Fatalf("wrong version/variant in %q", id)
	}
}

func TestMigrateAndAuthRepository(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migration must be idempotent: %v", err)
	}
	if count, err := db.CountAdmins(ctx); err != nil || count != 0 {
		t.Fatalf("admin count = %d, %v", count, err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	token := SetupToken{ID: mustID(t), TokenHash: []byte("hash"), ExpiresAt: now.Add(time.Minute), CreatedAt: now}
	if err := db.ReplaceSetupToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.SetupTokenByHash(ctx, token.TokenHash)
	if err != nil || loaded.ID != token.ID || !loaded.ExpiresAt.Equal(token.ExpiresAt) {
		t.Fatalf("loaded token = %+v, %v", loaded, err)
	}
}

func TestBodyRefreshMigrationPreservesMessages(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	goose.SetBaseFS(appMigrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, database.DB(), ".", 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	statements := []string{
		`INSERT INTO accounts (id, provider, display_name, email, color, status, auth_type, created_at, updated_at) VALUES ('account', 'imap', 'Work', 'work@example.com', '#315B7D', 'ready', 'password', ?, ?)`,
		`INSERT INTO folders (id, account_id, remote_name, display_name, role, role_source, created_at, updated_at) VALUES ('folder', 'account', 'INBOX', 'INBOX', 'inbox', 'provider_preset', ?, ?)`,
		`INSERT INTO conversations (id, account_id, thread_key, subject, preview, latest_at, message_count, unread_count, starred, created_at, updated_at) VALUES ('conversation', 'account', 'thread', 'Subject', '', ?, 1, 1, 0, ?, ?)`,
		`INSERT INTO messages (id, account_id, conversation_id, received_at, created_at, updated_at) VALUES ('message', 'account', 'conversation', ?, ?, ?)`,
		`INSERT INTO sync_checkpoints (id, account_id, folder_id, uid_validity, last_uid, state, updated_at) VALUES ('checkpoint', 'account', 'folder', 1, 7, 'idle', ?)`,
	}
	for _, statement := range statements {
		arguments := []any{now, now}
		if strings.Contains(statement, "conversations") || strings.Contains(statement, "messages") {
			arguments = []any{now, now, now}
		}
		if strings.Contains(statement, "sync_checkpoints") {
			arguments = []any{now}
		}
		if _, err := database.DB().ExecContext(ctx, statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var messages, refreshRequired int
	if err := database.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := database.DB().QueryRowContext(ctx, `SELECT body_refresh_required FROM sync_checkpoints WHERE id = 'checkpoint'`).Scan(&refreshRequired); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || refreshRequired != 1 {
		t.Fatalf("migration result messages=%d body_refresh_required=%d", messages, refreshRequired)
	}
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
