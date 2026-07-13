package mailruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
	"mailmanager/internal/accountsecret"
	"mailmanager/internal/connectors"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/events"
	"mailmanager/internal/ingeststore"
	"mailmanager/internal/repository"
	"mailmanager/internal/store"
	mailSync "mailmanager/internal/sync"
)

type fakeOAuthRefresher struct {
	mu        sync.Mutex
	refreshed *oauth2.Token
	err       error
	calls     int
}

func (f *fakeOAuthRefresher) RefreshToken(context.Context, accounts.Provider, *oauth2.Token) (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.refreshed == nil {
		return nil, f.err
	}
	copy := *f.refreshed
	return &copy, f.err
}

type fakeIMAPDialer struct {
	mu      sync.Mutex
	session connectors.IMAPSession
	err     error
	calls   int
}

type blockingIMAPDialer struct {
	mu      sync.Mutex
	gate    <-chan struct{}
	active  int
	peak    int
	started chan struct{}
}

func (d *blockingIMAPDialer) Dial(ctx context.Context, _ accounts.Config) (connectors.IMAPSession, error) {
	d.mu.Lock()
	d.active++
	if d.active > d.peak {
		d.peak = d.active
	}
	d.mu.Unlock()
	select {
	case d.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		d.closed()
		return nil, ctx.Err()
	case <-d.gate:
		return &trackedIMAPSession{fakeIMAPSession: fakeIMAPSession{
			mailboxes: []connectors.RemoteMailbox{}, state: connectors.MailboxState{UIDValidity: 1, UIDNext: 1},
		}, close: d.closed}, nil
	}
}

func (d *blockingIMAPDialer) closed() {
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
}

type trackedIMAPSession struct {
	fakeIMAPSession
	once  sync.Once
	close func()
}

func (s *trackedIMAPSession) Close() error {
	s.once.Do(s.close)
	return nil
}

func (d *fakeIMAPDialer) Dial(context.Context, accounts.Config) (connectors.IMAPSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return d.session, d.err
}

type fakeIMAPSession struct {
	mu           sync.Mutex
	capabilities connectors.IMAPCapabilities
	mailboxes    []connectors.RemoteMailbox
	state        connectors.MailboxState
	selectStates map[string]connectors.MailboxState
	selectErrors map[string]error
	selectCalls  []string
	fetchPart    []byte
	fetchErr     error
	fetchCalls   int
	moveCalls    int
	copyCalls    int
	deleteCalls  int
	flagCalls    int
}

func (s *fakeIMAPSession) Capabilities(context.Context) (connectors.IMAPCapabilities, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capabilities, nil
}

func (s *fakeIMAPSession) ListMailboxes(context.Context) ([]connectors.RemoteMailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]connectors.RemoteMailbox(nil), s.mailboxes...), nil
}

func (s *fakeIMAPSession) Select(_ context.Context, name string, _ bool) (connectors.MailboxState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectCalls = append(s.selectCalls, name)
	if err := s.selectErrors[name]; err != nil {
		return connectors.MailboxState{}, err
	}
	if state, ok := s.selectStates[name]; ok {
		state.Name = name
		return state, nil
	}
	state := s.state
	state.Name = name
	return state, nil
}

func (s *fakeIMAPSession) SearchUIDs(context.Context, connectors.SearchRequest) ([]uint32, error) {
	return nil, nil
}

func (s *fakeIMAPSession) FetchMessages(context.Context, connectors.FetchRequest) ([]connectors.RemoteMessage, error) {
	return nil, nil
}

func (s *fakeIMAPSession) FetchMessageStates(context.Context, connectors.FetchRequest) ([]connectors.RemoteMessageState, error) {
	return nil, nil
}

func (s *fakeIMAPSession) FetchBody(context.Context, uint32, int64) ([]byte, error) {
	return nil, nil
}

func (s *fakeIMAPSession) FetchPart(context.Context, uint32, []int, int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchCalls++
	return append([]byte(nil), s.fetchPart...), s.fetchErr
}

func (s *fakeIMAPSession) Idle(ctx context.Context) (connectors.IMAPEvent, error) {
	<-ctx.Done()
	return connectors.IMAPEvent{}, ctx.Err()
}

func (s *fakeIMAPSession) StoreFlags(context.Context, []uint32, connectors.FlagMutation, []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flagCalls++
	return nil
}

func (s *fakeIMAPSession) Copy(_ context.Context, uids []uint32, _ string) (connectors.CopyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.copyCalls++
	return connectors.CopyResult{UIDValidity: 1, SourceUIDs: append([]uint32(nil), uids...), DestinationUIDs: append([]uint32(nil), uids...)}, nil
}

func (s *fakeIMAPSession) Move(_ context.Context, uids []uint32, _ string) (connectors.MoveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moveCalls++
	return connectors.MoveResult{UIDValidity: 1, SourceUIDs: append([]uint32(nil), uids...), DestinationUIDs: append([]uint32(nil), uids...)}, nil
}

func (s *fakeIMAPSession) Delete(context.Context, []uint32, bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls++
	return nil
}

func (s *fakeIMAPSession) Close() error { return nil }

type fakeMailSender struct {
	mu       sync.Mutex
	result   connectors.DeliveryResult
	err      error
	envelope connectors.Envelope
	calls    int
}

func (s *fakeMailSender) Send(_ context.Context, _ connectors.SMTPConfig, envelope connectors.Envelope) (connectors.DeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.envelope = envelope
	return s.result, s.err
}

type runtimeFixture struct {
	runtime  *Runtime
	database *store.Store
	repo     *repository.Repository
	cipher   *cryptox.Cipher
	oauth    *fakeOAuthRefresher
	session  *fakeIMAPSession
	dialer   *fakeIMAPDialer
	sender   *fakeMailSender
	drafts   string
	cache    string
}

func newRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptox.NewCipher(bytes.Repeat([]byte{0x5a}, cryptox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	session := &fakeIMAPSession{
		mailboxes: []connectors.RemoteMailbox{{Name: "INBOX", Selectable: true, Role: accounts.FolderInbox}},
		state:     connectors.MailboxState{UIDValidity: 1, UIDNext: 2},
	}
	dialer := &fakeIMAPDialer{session: session}
	sender := &fakeMailSender{result: connectors.DeliveryResult{Status: connectors.DeliveryFailed, Stage: connectors.StageConnect}, err: errors.New("not configured")}
	oauth := &fakeOAuthRefresher{}
	repo := repository.New(database.DB())
	runtime, err := New(Options{
		Store: database, Repository: repo, IngestStore: ingeststore.New(database), Cipher: cipher,
		OAuth: oauth, Events: events.NewHub(32), IMAPDialer: dialer, SMTPSender: sender,
		AttachmentCacheDir: filepath.Join(directory, "cache"), DraftBlobDir: filepath.Join(directory, "drafts"),
		MaxAttachmentBytes: 1 << 20, AttachmentCacheQuotaBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtime.cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtime.draftBlobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return runtimeFixture{runtime: runtime, database: database, repo: repo, cipher: cipher, oauth: oauth, session: session, dialer: dialer, sender: sender, drafts: runtime.draftBlobDir, cache: runtime.cacheDir}
}

func TestAccountTestSelectsInboxAndPropagatesFailure(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	fixture.session.selectErrors = map[string]error{"INBOX": errors.New("unsafe login")}

	err := fixture.runtime.TestAccount(context.Background(), account.ID)
	if err == nil || !strings.Contains(err.Error(), "unsafe login") {
		t.Fatalf("TestAccount() error = %v", err)
	}
	if len(fixture.session.selectCalls) != 1 || fixture.session.selectCalls[0] != "INBOX" {
		t.Fatalf("Select calls = %v, want INBOX", fixture.session.selectCalls)
	}
}

func (f runtimeFixture) createPasswordAccount(t *testing.T) (repository.AccountSummary, string) {
	return f.createPasswordAccountEmail(t, "owner@example.com")
}

func (f runtimeFixture) createPasswordAccountEmail(t *testing.T, email string) (repository.AccountSummary, string) {
	t.Helper()
	credential, err := accountsecret.Marshal(accountsecret.Credential{
		Username: email, Secret: "application-password",
		IMAPTLSMode: accounts.TLSImplicit, SMTPTLSMode: accounts.TLSImplicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := f.cipher.Seal(credential, accountsecret.Purpose("imap", email))
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.repo.CreateAccount(context.Background(), repository.AccountInput{
		DisplayName: "Owner", Email: email, Provider: "imap", Color: "#336699",
		AuthType: "password", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465, CredentialEncrypted: encrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	identityID, err := f.repo.PrimaryIdentityID(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	return account, identityID
}

func (f runtimeFixture) createExpiredGoogleOAuthAccount(t *testing.T, accessToken, refreshToken string) (repository.AccountSummary, string) {
	t.Helper()
	preset, err := accounts.PresetFor(accounts.ProviderGoogle)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := accountsecret.MarshalOAuth(accountsecret.OAuthCredential{
		Username: "owner@gmail.com",
		Token: &oauth2.Token{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			Expiry:       time.Now().Add(-time.Hour),
		},
		IMAPTLSMode: preset.IMAP.TLSMode,
		SMTPTLSMode: preset.SMTP.TLSMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	purpose := accountsecret.Purpose("google", "owner@gmail.com")
	encrypted, err := f.cipher.Seal(credential, purpose)
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.repo.CreateAccount(context.Background(), repository.AccountInput{
		DisplayName: "Gmail", Email: "owner@gmail.com", Provider: "google", Color: "#336699", AuthType: "oauth2",
		IMAPHost: preset.IMAP.Host, IMAPPort: int(preset.IMAP.Port), SMTPHost: preset.SMTP.Host, SMTPPort: int(preset.SMTP.Port), OAuthTokenEncrypted: encrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return account, purpose
}

func TestSyncQueueDeduplicatesAndWorkersStayWithinLimit(t *testing.T) {
	fixture := newRuntimeFixture(t)
	job := syncJob{accountID: "account", kind: syncDiscover}
	for range 20 {
		if err := fixture.runtime.enqueueSync(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.runtime.syncHigh) != 1 || len(fixture.runtime.pending) != 1 || !fixture.runtime.pending[job.key()].rerun {
		t.Fatalf("sync queue was not deduplicated: queued=%d pending=%d", len(fixture.runtime.syncHigh), len(fixture.runtime.pending))
	}
	// Reset the direct queue probe before exercising the real worker pool.
	fixture.runtime.syncHigh = make(chan syncJob, 256)
	fixture.runtime.pending = make(map[string]*syncJobState)
	for index := range 8 {
		fixture.createPasswordAccountEmail(t, fmt.Sprintf("owner%d@example.com", index))
	}
	gate := make(chan struct{})
	dialer := &blockingIMAPDialer{gate: gate, started: make(chan struct{}, 16)}
	fixture.runtime.imapDialer = dialer
	ctx, cancel := context.WithCancel(context.Background())
	if err := fixture.runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer fixture.runtime.Close()
	for range 4 {
		select {
		case <-dialer.started:
		case <-time.After(2 * time.Second):
			t.Fatal("sync workers did not start")
		}
	}
	time.Sleep(50 * time.Millisecond)
	dialer.mu.Lock()
	peak := dialer.peak
	dialer.mu.Unlock()
	if peak != 4 {
		t.Fatalf("peak concurrent sync workers = %d, want 4", peak)
	}
	close(gate)
	cancel()
}

func TestSyncWorkersDoNotDeadlockWhenDiscoveryAndBackfillExceedQueueCapacity(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	const folderCount = 320
	mailboxes := make([]connectors.RemoteMailbox, folderCount)
	for index := range mailboxes {
		mailboxes[index] = connectors.RemoteMailbox{
			Name:       fmt.Sprintf("Folder-%03d", index),
			Selectable: true,
			Role:       accounts.FolderUnknown,
		}
	}
	sort.Slice(mailboxes, func(i, j int) bool { return mailboxes[i].Name < mailboxes[j].Name })
	fixture.session.mu.Lock()
	fixture.session.mailboxes = mailboxes
	fixture.session.state = connectors.MailboxState{UIDValidity: 1, UIDNext: 1}
	fixture.session.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	if err := fixture.runtime.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		fixture.runtime.Close()
	}()

	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var total, completed int
		err := fixture.database.DB().QueryRowContext(context.Background(), `
			SELECT COUNT(*), COALESCE(SUM(CASE WHEN backfill_before IS NULL THEN 1 ELSE 0 END), 0)
			FROM sync_checkpoints WHERE account_id = ?`, account.ID).Scan(&total, &completed)
		if err != nil {
			t.Fatal(err)
		}
		fixture.runtime.syncMu.Lock()
		pending := len(fixture.runtime.pending)
		fixture.runtime.syncMu.Unlock()
		if total == folderCount && completed == folderCount && pending == 0 {
			if len(fixture.runtime.syncHigh) != 0 || len(fixture.runtime.syncLow) != 0 {
				t.Fatalf("completed sync left queued work: high=%d low=%d", len(fixture.runtime.syncHigh), len(fixture.runtime.syncLow))
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("sync stalled: folders=%d completed_backfills=%d pending=%d high=%d low=%d",
				total, completed, pending, len(fixture.runtime.syncHigh), len(fixture.runtime.syncLow))
		case <-ticker.C:
		}
	}
}

func TestAccountConfigRefreshesExpiredOAuthAndPersistsIt(t *testing.T) {
	fixture := newRuntimeFixture(t)
	preset, err := accounts.PresetFor(accounts.ProviderGoogle)
	if err != nil {
		t.Fatal(err)
	}
	expired := &oauth2.Token{AccessToken: "expired", RefreshToken: "refresh-token", Expiry: time.Now().Add(-time.Hour)}
	credential, err := accountsecret.MarshalOAuth(accountsecret.OAuthCredential{
		Username: "owner@gmail.com", Token: expired,
		IMAPTLSMode: preset.IMAP.TLSMode, SMTPTLSMode: preset.SMTP.TLSMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	purpose := accountsecret.Purpose("google", "owner@gmail.com")
	encrypted, err := fixture.cipher.Seal(credential, purpose)
	if err != nil {
		t.Fatal(err)
	}
	account, err := fixture.repo.CreateAccount(context.Background(), repository.AccountInput{
		DisplayName: "Gmail", Email: "owner@gmail.com", Provider: "google", Color: "#336699", AuthType: "oauth2",
		IMAPHost: preset.IMAP.Host, IMAPPort: int(preset.IMAP.Port), SMTPHost: preset.SMTP.Host, SMTPPort: int(preset.SMTP.Port), OAuthTokenEncrypted: encrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.oauth.refreshed = &oauth2.Token{AccessToken: "fresh-access", Expiry: time.Now().Add(time.Hour)}

	config, err := fixture.runtime.accountConfig(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if config.Credentials.Secret != "fresh-access" || fixture.oauth.calls != 1 {
		t.Fatalf("refresh result = %q, calls = %d", config.Credentials.Secret, fixture.oauth.calls)
	}
	record, err := fixture.repo.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := fixture.cipher.Open(record.OAuthTokenEncrypted, purpose)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := accountsecret.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Token.AccessToken != "fresh-access" || persisted.Token.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected persisted token: %#v", persisted.Token)
	}
}

func TestAccountConfigOAuthTokenPersistenceFailureRequiresReauthentication(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, purpose := fixture.createExpiredGoogleOAuthAccount(t, "expired-access", "old-refresh-token")
	fixture.oauth.refreshed = &oauth2.Token{
		AccessToken:  "rotated-access-token",
		RefreshToken: "rotated-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	if _, err := fixture.database.DB().ExecContext(context.Background(), `
		CREATE TRIGGER fail_oauth_token_update
		BEFORE UPDATE OF oauth_token_encrypted ON accounts
		BEGIN SELECT RAISE(FAIL, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	config, err := fixture.runtime.accountConfig(context.Background(), account.ID)
	if err == nil {
		t.Fatal("accountConfig succeeded after the refreshed token failed to persist")
	}
	if !errors.Is(err, errOAuthRefresh) || !errors.Is(err, errOAuthTokenPersist) {
		t.Fatalf("accountConfig error = %v, want OAuth refresh and token persistence sentinels", err)
	}
	if config.Credentials.Secret != "" {
		t.Fatalf("accountConfig returned the unpersisted access token %q", config.Credentials.Secret)
	}
	record, err := fixture.repo.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "reauth_required" || record.LastErrorCode != "oauth_token_persist_failed" {
		t.Fatalf("account status = %q/%q, want reauth_required/oauth_token_persist_failed", record.Status, record.LastErrorCode)
	}
	plaintext, err := fixture.cipher.Open(record.OAuthTokenEncrypted, purpose)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := accountsecret.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Token.AccessToken != "expired-access" || persisted.Token.RefreshToken != "old-refresh-token" {
		t.Fatalf("stored token changed after failed update: %#v", persisted.Token)
	}

	if _, err := fixture.runtime.accountConfig(context.Background(), account.ID); !errors.Is(err, errOAuthTokenPersist) {
		t.Fatalf("second accountConfig error = %v, want token persistence sentinel", err)
	}
	if fixture.oauth.calls != 1 {
		t.Fatalf("OAuth refresher calls = %d, want 1", fixture.oauth.calls)
	}
}

func TestAccountConfigOAuthTokenAndStatusPersistenceFailuresAreExplicit(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createExpiredGoogleOAuthAccount(t, "expired-access", "old-refresh-token")
	fixture.oauth.refreshed = &oauth2.Token{
		AccessToken:  "rotated-access-secret",
		RefreshToken: "rotated-refresh-secret",
		Expiry:       time.Now().Add(time.Hour),
	}
	if _, err := fixture.database.DB().ExecContext(context.Background(), `
		CREATE TRIGGER fail_all_account_updates
		BEFORE UPDATE ON accounts
		BEGIN SELECT RAISE(FAIL, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	config, err := fixture.runtime.accountConfig(context.Background(), account.ID)
	if err == nil {
		t.Fatal("accountConfig succeeded after both account updates failed")
	}
	if !errors.Is(err, errOAuthRefresh) || !errors.Is(err, errOAuthTokenPersist) {
		t.Fatalf("accountConfig error = %v, want OAuth refresh and token persistence sentinels", err)
	}
	if config.Credentials.Secret != "" {
		t.Fatalf("accountConfig returned the unpersisted access token %q", config.Credentials.Secret)
	}
	errorText := err.Error()
	for _, fragment := range []string{"persist refreshed OAuth credential", "persist OAuth reauthentication status"} {
		if !strings.Contains(errorText, fragment) {
			t.Fatalf("accountConfig error %q does not contain %q", errorText, fragment)
		}
	}
	for _, secret := range []string{"rotated-access-secret", "rotated-refresh-secret"} {
		if strings.Contains(errorText, secret) {
			t.Fatalf("accountConfig error leaked token value %q: %s", secret, errorText)
		}
	}
}

func TestArchiveWithoutSafeMoveNeedsAttention(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "INBOX", Selectable: true, Role: accounts.FolderInbox},
		{Name: "Archive", Selectable: true, Role: accounts.FolderArchive},
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := folders[0]
	if inbox.Role != "inbox" {
		inbox = folders[1]
	}
	if err := fixture.runtime.ingest.StoreMessages(context.Background(), syncMessageBatch(account.ID, inbox.ID, 1, 7, nil)); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := fixture.database.DB().QueryRow(`SELECT message_id FROM message_locations WHERE folder_id = ?`, inbox.ID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.repo.CreateOperation(context.Background(), account.ID, "archive", []string{messageID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.DB().Exec(`UPDATE operations SET execute_after = 0 WHERE id = ?`, operation.ID); err != nil {
		t.Fatal(err)
	}
	work, err := fixture.runtime.claimOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOperation(context.Background(), work)
	var state, code string
	if err := fixture.database.DB().QueryRow(`SELECT status, error_code FROM operations WHERE id = ?`, operation.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "needs_attention" || code != "safe_move_unsupported" {
		t.Fatalf("operation state = %q, code = %q", state, code)
	}
	if fixture.session.moveCalls != 0 || fixture.session.copyCalls != 0 || fixture.session.deleteCalls != 0 {
		t.Fatal("unsafe IMAP mutation was attempted")
	}
}

func TestMovePersistsFirstSourceWhenSecondSelectFails(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "Source A", Selectable: true, Role: accounts.FolderUnknown},
		{Name: "Source B", Selectable: true, Role: accounts.FolderUnknown},
		{Name: "Archive", Selectable: true, Role: accounts.FolderArchive},
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := foldersByName(folders)
	sources := []ingeststore.FolderRecord{byName["Source A"], byName["Source B"]}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	firstMessageID := storeOperationMessage(t, fixture, account.ID, sources[0].ID, 11)
	secondMessageID := storeOperationMessage(t, fixture, account.ID, sources[1].ID, 12)
	operation, err := fixture.repo.CreateOperation(context.Background(), account.ID, "move", []string{firstMessageID, secondMessageID}, byName["Archive"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.DB().Exec(`UPDATE operations SET execute_after = 0 WHERE id = ?`, operation.ID); err != nil {
		t.Fatal(err)
	}
	fixture.session.capabilities = connectors.IMAPCapabilities{Move: true}
	fixture.session.selectErrors = map[string]error{sources[1].RemoteName: errors.New("second source unavailable")}
	enableOperationSyncQueue(t, fixture.runtime)

	work, err := fixture.runtime.claimOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOperation(context.Background(), work)

	assertOperationState(t, fixture, operation.ID, "unknown", "select_failed")
	assertLocationCount(t, fixture, firstMessageID, sources[0].ID, 0)
	assertLocationCount(t, fixture, firstMessageID, byName["Archive"].ID, 1)
	assertLocationCount(t, fixture, secondMessageID, sources[1].ID, 1)
	if fixture.session.moveCalls != 1 {
		t.Fatalf("MOVE calls = %d, want 1", fixture.session.moveCalls)
	}
	assertOperationSyncQueued(t, fixture.runtime, account.ID)
}

func TestDeletePersistsFirstSourceWhenSecondUIDValidityChanges(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "Trash A", Selectable: true, Role: accounts.FolderTrash},
		{Name: "Trash B", Selectable: true, Role: accounts.FolderTrash},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources := append([]ingeststore.FolderRecord(nil), folders...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	firstMessageID := storeOperationMessage(t, fixture, account.ID, sources[0].ID, 21)
	secondMessageID := storeOperationMessage(t, fixture, account.ID, sources[1].ID, 22)
	operation, err := fixture.repo.CreateOperation(context.Background(), account.ID, "delete", []string{firstMessageID, secondMessageID}, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.capabilities = connectors.IMAPCapabilities{UIDPlus: true}
	fixture.session.selectStates = map[string]connectors.MailboxState{
		sources[1].RemoteName: {UIDValidity: 2, UIDNext: 2},
	}
	enableOperationSyncQueue(t, fixture.runtime)

	work, err := fixture.runtime.claimOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOperation(context.Background(), work)

	assertOperationState(t, fixture, operation.ID, "needs_attention", "uidvalidity_changed")
	assertMessageCount(t, fixture, firstMessageID, 0)
	assertMessageCount(t, fixture, secondMessageID, 1)
	if fixture.session.deleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want 1", fixture.session.deleteCalls)
	}
	assertOperationSyncQueued(t, fixture.runtime, account.ID)
}

func TestOutboxUnknownRetainsDraftAndAttachment(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, identityID := fixture.createPasswordAccount(t)
	draft, err := fixture.repo.CreateDraft(context.Background(), repository.DraftInput{
		AccountID: account.ID, IdentityID: identityID,
		To:  []repository.Address{{Name: "Recipient", Email: "to@example.com"}},
		BCC: []repository.Address{{Email: "blind@example.com"}}, Subject: "Delivery semantics", BodyHTML: "<p>Hello</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("durable attachment")
	directory := filepath.Join(fixture.drafts, draft.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(directory, "attachment.blob")
	if err := os.WriteFile(blob, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if _, err := fixture.repo.AddDraftAttachment(context.Background(), draft.ID, "report.txt", "text/plain", blob, int64(len(data)), digest[:]); err != nil {
		t.Fatal(err)
	}
	outboxID, err := fixture.repo.QueueDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.sender.result = connectors.DeliveryResult{Status: connectors.DeliveryUnknown, Stage: connectors.StageCommit}
	fixture.sender.err = io.ErrUnexpectedEOF
	work, err := fixture.runtime.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOutbox(context.Background(), work)
	var state string
	if err := fixture.database.DB().QueryRow(`SELECT state FROM outbox WHERE id = ?`, outboxID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("outbox state = %q", state)
	}
	if _, err := fixture.repo.GetDraft(context.Background(), draft.ID); err != nil {
		t.Fatalf("draft was removed after unknown delivery: %v", err)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("draft attachment was removed after unknown delivery: %v", err)
	}
	message := string(fixture.sender.envelope.Message)
	if strings.Contains(strings.ToLower(message), "\r\nbcc:") {
		t.Fatal("Bcc leaked into MIME headers")
	}
	if len(fixture.sender.envelope.Recipients) != 2 {
		t.Fatalf("envelope recipients = %#v", fixture.sender.envelope.Recipients)
	}
}

func TestLoadAttachmentValidatesUIDValidityAndCachesDecodedPart(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{{Name: "INBOX", Selectable: true, Role: accounts.FolderInbox}})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("decoded attachment payload")
	parts := []connectors.RemotePart{{Path: []int{2}, MediaType: "application/octet-stream", Disposition: "attachment", Filename: "data.bin", Size: uint32(len(payload))}}
	if err := fixture.runtime.ingest.StoreMessages(context.Background(), syncMessageBatch(account.ID, folders[0].ID, 1, 9, parts)); err != nil {
		t.Fatal(err)
	}
	var attachmentID string
	if err := fixture.database.DB().QueryRow(`SELECT id FROM attachments LIMIT 1`).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	record, err := fixture.repo.GetAttachment(context.Background(), attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.state.UIDValidity = 2
	fixture.session.fetchPart = payload
	if _, err := fixture.runtime.LoadAttachment(context.Background(), record); err == nil || !strings.Contains(err.Error(), "UIDVALIDITY") {
		t.Fatalf("expected UIDVALIDITY error, got %v", err)
	}
	if fixture.session.fetchCalls != 0 {
		t.Fatal("attachment was fetched after UIDVALIDITY mismatch")
	}
	fixture.session.state.UIDValidity = 1
	path, err := fixture.runtime.LoadAttachment(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, payload) {
		t.Fatalf("cached payload = %q", cached)
	}
	updated, err := fixture.repo.GetAttachment(context.Background(), attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CachePath != path || fixture.session.fetchCalls != 1 {
		t.Fatalf("cache path = %q, fetch calls = %d", updated.CachePath, fixture.session.fetchCalls)
	}
	var storedDigest []byte
	if err := fixture.database.DB().QueryRow(`SELECT cache_sha256 FROM attachments WHERE id = ?`, attachmentID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(payload)
	if !bytes.Equal(storedDigest, wantDigest[:]) {
		t.Fatal("cached attachment digest was not persisted")
	}
	entries, err := os.ReadDir(fixture.cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".attachment-") {
			t.Fatalf("temporary cache file was left behind: %s", entry.Name())
		}
	}
}

func TestCacheQuotaEvictsOldestAndClearsDatabaseReference(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{{Name: "INBOX", Selectable: true, Role: accounts.FolderInbox}})
	if err != nil {
		t.Fatal(err)
	}
	parts := []connectors.RemotePart{{Path: []int{2}, MediaType: "application/octet-stream", Disposition: "attachment", Filename: "old.bin", Size: 8}}
	if err := fixture.runtime.ingest.StoreMessages(context.Background(), syncMessageBatch(account.ID, folders[0].ID, 1, 10, parts)); err != nil {
		t.Fatal(err)
	}
	var attachmentID string
	if err := fixture.database.DB().QueryRow(`SELECT id FROM attachments LIMIT 1`).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(fixture.cache, "old.blob")
	oldData := []byte("12345678")
	if err := os.WriteFile(oldPath, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(oldData)
	if err := fixture.repo.SetAttachmentCache(context.Background(), attachmentID, oldPath, oldDigest[:]); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.cacheQuotaBytes = 12
	if err := fixture.runtime.ensureCacheCapacity(context.Background(), 8, filepath.Join(fixture.cache, "new.blob")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old cache file was not evicted: %v", err)
	}
	record, err := fixture.repo.GetAttachment(context.Background(), attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.CachePath != "" {
		t.Fatalf("stale cache reference remains: %q", record.CachePath)
	}
}

func syncMessageBatch(accountID, folderID string, uidValidity, uid uint32, parts []connectors.RemotePart) mailSync.MessageBatch {
	return mailSync.MessageBatch{
		AccountID: accountID, FolderID: folderID, UIDValidity: uidValidity,
		Messages: []connectors.RemoteMessage{{
			UID: uid, InternalDate: time.Now().UTC(), RFC822Size: 100,
			Envelope: connectors.RemoteEnvelope{
				Date: time.Now().UTC(), Subject: "Message", MessageID: "<message@example.com>",
				From: []connectors.RemoteAddress{{Email: "sender@example.com"}},
				To:   []connectors.RemoteAddress{{Email: "owner@example.com"}},
			},
			Parts: parts, TextBody: "Body",
		}},
	}
}

func foldersByName(folders []ingeststore.FolderRecord) map[string]ingeststore.FolderRecord {
	result := make(map[string]ingeststore.FolderRecord, len(folders))
	for _, folder := range folders {
		result[folder.RemoteName] = folder
	}
	return result
}

func storeOperationMessage(t *testing.T, fixture runtimeFixture, accountID, folderID string, uid uint32) string {
	t.Helper()
	batch := syncMessageBatch(accountID, folderID, 1, uid, nil)
	batch.Messages[0].Envelope.MessageID = fmt.Sprintf("<operation-%d@example.com>", uid)
	if err := fixture.runtime.ingest.StoreMessages(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := fixture.database.DB().QueryRow(`SELECT message_id FROM message_locations WHERE folder_id = ? AND uid = ?`, folderID, uid).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	return messageID
}

func enableOperationSyncQueue(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runtime.startMu.Lock()
	runtime.started = true
	runtime.ctx = ctx
	runtime.cancel = cancel
	runtime.startMu.Unlock()
	t.Cleanup(func() {
		cancel()
		runtime.startMu.Lock()
		runtime.started = false
		runtime.ctx = nil
		runtime.cancel = nil
		runtime.startMu.Unlock()
	})
}

func assertOperationState(t *testing.T, fixture runtimeFixture, operationID, wantState, wantCode string) {
	t.Helper()
	var state, code string
	if err := fixture.database.DB().QueryRow(`SELECT status, error_code FROM operations WHERE id = ?`, operationID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != wantState || code != wantCode {
		t.Fatalf("operation state = %q, code = %q; want %q/%q", state, code, wantState, wantCode)
	}
}

func assertLocationCount(t *testing.T, fixture runtimeFixture, messageID, folderID string, want int) {
	t.Helper()
	var count int
	if err := fixture.database.DB().QueryRow(`SELECT COUNT(*) FROM message_locations WHERE message_id = ? AND folder_id = ?`, messageID, folderID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("location count for message %s in folder %s = %d, want %d", messageID, folderID, count, want)
	}
}

func assertMessageCount(t *testing.T, fixture runtimeFixture, messageID string, want int) {
	t.Helper()
	var count int
	if err := fixture.database.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ?`, messageID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("message count for %s = %d, want %d", messageID, count, want)
	}
}

func assertOperationSyncQueued(t *testing.T, runtime *Runtime, accountID string) {
	t.Helper()
	key := (syncJob{accountID: accountID, kind: syncDiscover}).key()
	runtime.syncMu.Lock()
	_, pending := runtime.pending[key]
	runtime.syncMu.Unlock()
	if !pending || len(runtime.syncHigh) != 1 {
		t.Fatalf("operation convergence sync was not queued: pending=%v queued=%d", pending, len(runtime.syncHigh))
	}
}
