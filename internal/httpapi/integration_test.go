package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
	"mailmanager/internal/accountsecret"
	"mailmanager/internal/auth"
	"mailmanager/internal/connectors"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/events"
	"mailmanager/internal/oauthflow"
	"mailmanager/internal/repository"
	"mailmanager/internal/store"
	"mailmanager/internal/updater"
)

const testPublicURL = "https://mail.example.test"

type httpFixture struct {
	handler       http.Handler
	database      *store.Store
	repository    *repository.Repository
	cipher        *cryptox.Cipher
	runtime       *stubMailRuntime
	updater       *stubUpdateService
	bootstrap     auth.BootstrapToken
	bootstrapPath string
	now           time.Time
	cookies       []*http.Cookie
	csrfToken     string
}

type stubMailRuntime struct {
	mu                  sync.Mutex
	operationWakes      int
	outboxWakes         int
	attachmentPath      string
	attachmentErr       error
	attachmentLoads     int
	triggeredAccountIDs []string
}

type stubUpdateService struct {
	mu             sync.Mutex
	status         updater.Status
	checkErr       error
	installErr     error
	checks         int
	installVersion string
}

func (s *stubUpdateService) Status(context.Context) (updater.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, nil
}

func (s *stubUpdateService) Check(context.Context) (updater.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	return s.status, s.checkErr
}

func (s *stubUpdateService) Install(_ context.Context, target string) (updater.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installVersion = target
	if s.installErr == nil {
		s.status.State = updater.StateQueued
		s.status.TargetVersion = target
	}
	return s.status, s.installErr
}

func (r *stubMailRuntime) TestAccount(context.Context, string) error { return nil }

func (r *stubMailRuntime) TriggerSync(_ context.Context, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triggeredAccountIDs = append(r.triggeredAccountIDs, accountID)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (r *stubMailRuntime) WakeOperations() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.operationWakes++
}

func (r *stubMailRuntime) WakeOutbox() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outboxWakes++
}

func (r *stubMailRuntime) LoadAttachment(context.Context, repository.AttachmentRecord) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attachmentLoads++
	return r.attachmentPath, r.attachmentErr
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(root, "mailmanager.db"))
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
	cipher, err := cryptox.NewCipher(bytes.Repeat([]byte{0x42}, cryptox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	authService, err := auth.NewService(database, cipher, auth.Options{
		Now: func() time.Time { return now },
		PasswordParams: auth.PasswordParams{
			MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := authService.IssueBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(root, "bootstrap-token")
	if err := os.WriteFile(bootstrapPath, []byte(bootstrap.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	publicURL, err := url.Parse(testPublicURL)
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := oauthflow.New(database, cipher, publicURL)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.New(database.DB())
	runtime := &stubMailRuntime{}
	updateService := &stubUpdateService{status: updater.Status{
		CurrentVersion: "1.0.0", CurrentCommit: "test-commit", CurrentBuildTime: "2026-07-13T00:00:00Z",
		Enabled: true, InstallSupported: true, State: updater.StateIdle,
	}}
	handler, err := NewServer(ServerOptions{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		PublicURL:          testPublicURL,
		SecureCookies:      true,
		Auth:               authService,
		Repository:         repo,
		Cipher:             cipher,
		Events:             events.NewHub(32),
		Web:                http.NotFoundHandler(),
		Ping:               database.Ping,
		Runtime:            runtime,
		OAuth:              oauth,
		DraftBlobDir:       filepath.Join(root, "drafts"),
		MaxAttachmentBytes: 1 << 20,
		BootstrapTokenPath: bootstrapPath,
		Updater:            updateService,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &httpFixture{
		handler: handler, database: database, repository: repo, cipher: cipher, runtime: runtime, updater: updateService,
		bootstrap: bootstrap, bootstrapPath: bootstrapPath, now: now,
	}
}

func (f *httpFixture) request(t *testing.T, method, path string, body any, authenticated, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, testPublicURL+path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Origin", testPublicURL)
	}
	if authenticated {
		for _, cookie := range f.cookies {
			request.AddCookie(cookie)
		}
	}
	if csrf {
		request.Header.Set(auth.CSRFHeaderName, f.csrfToken)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *httpFixture) setupAndLogin(t *testing.T) auth.SetupResult {
	t.Helper()
	statusResponse := f.request(t, http.MethodGet, "/api/v1/setup/status", nil, false, false)
	assertStatus(t, statusResponse, http.StatusOK)
	var status auth.SetupStatus
	decodeResponse(t, statusResponse, &status)
	if status.Configured || status.TokenExpiresAt == nil {
		t.Fatalf("unexpected initial setup status: %+v", status)
	}

	enrollResponse := f.request(t, http.MethodPost, "/api/v1/setup/enroll", map[string]any{
		"bootstrap_token": f.bootstrap.Token,
		"username":        "admin",
	}, false, false)
	assertStatus(t, enrollResponse, http.StatusOK)
	var enrollment auth.SetupEnrollment
	decodeResponse(t, enrollResponse, &enrollment)
	if enrollment.Secret == "" || !strings.HasPrefix(enrollment.ProvisioningURI, "otpauth://") {
		t.Fatalf("invalid setup enrollment: %+v", enrollment)
	}
	code, err := auth.TOTPCode(enrollment.Secret, f.now)
	if err != nil {
		t.Fatal(err)
	}
	completeResponse := f.request(t, http.MethodPost, "/api/v1/setup/complete", map[string]any{
		"bootstrap_token": f.bootstrap.Token,
		"password":        "correct horse battery staple",
		"totp_code":       code,
	}, false, false)
	assertStatus(t, completeResponse, http.StatusCreated)
	var setup auth.SetupResult
	decodeResponse(t, completeResponse, &setup)
	if len(setup.RecoveryCodes) != 10 {
		t.Fatalf("recovery code count = %d", len(setup.RecoveryCodes))
	}
	for _, cookie := range completeResponse.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			t.Fatal("setup completion must not silently establish a session")
		}
	}
	if _, err := os.Stat(f.bootstrapPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap token file still exists: %v", err)
	}

	loginResponse := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin",
		"password": "correct horse battery staple",
	}, false, false)
	assertStatus(t, loginResponse, http.StatusOK)
	var challenge struct {
		ChallengeToken string `json:"challenge_token"`
		ChallengeID    string `json:"challenge_id"`
		TOTPRequired   bool   `json:"totp_required"`
	}
	decodeResponse(t, loginResponse, &challenge)
	if challenge.ChallengeToken == "" || challenge.ChallengeID != challenge.ChallengeToken || !challenge.TOTPRequired {
		t.Fatalf("unexpected password challenge: %+v", challenge)
	}
	totpResponse := f.request(t, http.MethodPost, "/api/v1/auth/totp", map[string]any{
		"challenge_token": challenge.ChallengeToken,
		"code":            code,
	}, false, false)
	assertStatus(t, totpResponse, http.StatusOK)
	f.cookies = totpResponse.Result().Cookies()
	for _, cookie := range f.cookies {
		if cookie.Name == auth.CSRFCookieName {
			f.csrfToken = cookie.Value
		}
		if (cookie.Name == auth.SessionCookieName || cookie.Name == auth.CSRFCookieName) && (!cookie.Secure || cookie.SameSite != http.SameSiteStrictMode) {
			t.Fatalf("insecure auth cookie: %+v", cookie)
		}
	}
	if f.csrfToken == "" || cookieNamed(f.cookies, auth.SessionCookieName) == nil {
		t.Fatalf("login did not issue both auth cookies: %+v", f.cookies)
	}
	return setup
}

func TestHTTPSetupLoginSessionAndCSRF(t *testing.T) {
	fixture := newHTTPFixture(t)
	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/accounts", nil, false, false)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "session_required")

	setup := fixture.setupAndLogin(t)
	sessionResponse := fixture.request(t, http.MethodGet, "/api/v1/auth/session", nil, true, false)
	assertStatus(t, sessionResponse, http.StatusOK)
	var principal auth.Principal
	decodeResponse(t, sessionResponse, &principal)
	if principal.AdminID != setup.AdminID || principal.Username != "admin" {
		t.Fatalf("unexpected principal: %+v", principal)
	}

	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/accounts", map[string]any{}, true, false)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_invalid")
	request := httptest.NewRequest(http.MethodPost, testPublicURL+"/api/v1/accounts", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testPublicURL)
	request.Header.Set(auth.CSRFHeaderName, "wrong-token")
	for _, cookie := range fixture.cookies {
		request.AddCookie(cookie)
	}
	wrongCSRF := httptest.NewRecorder()
	fixture.handler.ServeHTTP(wrongCSRF, request)
	assertAPIError(t, wrongCSRF, http.StatusForbidden, "csrf_invalid")

	logout := fixture.request(t, http.MethodDelete, "/api/v1/auth/session", nil, true, true)
	assertStatus(t, logout, http.StatusNoContent)
	for _, cookie := range logout.Result().Cookies() {
		if (cookie.Name == auth.SessionCookieName || cookie.Name == auth.CSRFCookieName) && cookie.MaxAge >= 0 {
			t.Fatalf("logout did not expire cookie: %+v", cookie)
		}
	}
	expired := fixture.request(t, http.MethodGet, "/api/v1/auth/session", nil, true, false)
	assertAPIError(t, expired, http.StatusUnauthorized, "session_expired")
}

func TestHTTPAccountCreateAndUpdate(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	payload := validAccountPayload()
	payload["signature_html"] = `<p>Regards</p><script>alert("x")</script>`
	response := fixture.request(t, http.MethodPost, "/api/v1/accounts", payload, true, true)
	assertStatus(t, response, http.StatusCreated)
	if strings.Contains(response.Body.String(), "app-password") {
		t.Fatal("account response leaked the credential secret")
	}
	var created repository.AccountSummary
	decodeResponse(t, response, &created)
	if created.Provider != "imap" || created.AuthType != "password" || created.Color != "#31A36D" ||
		created.Username != "owner@example.com" || created.IMAP.Host != "imap.example.com" || created.IMAP.Port != 993 || created.IMAP.TLSMode != "implicit" ||
		created.SMTP.Host != "smtp.example.com" || created.SMTP.Port != 465 || created.SMTP.TLSMode != "implicit" {
		t.Fatalf("unexpected created account: %+v", created)
	}
	fixture.runtime.mu.Lock()
	triggered := append([]string(nil), fixture.runtime.triggeredAccountIDs...)
	fixture.runtime.mu.Unlock()
	if len(triggered) != 1 || triggered[0] != created.ID {
		t.Fatalf("account creation sync triggers = %v", triggered)
	}
	record, err := fixture.repository.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := fixture.cipher.Open(record.CredentialEncrypted, accountsecret.Purpose("imap", "owner@example.com"))
	if err != nil || !strings.Contains(string(plaintext), "app-password") {
		t.Fatalf("stored credential was not encrypted with the account purpose: %v", err)
	}
	var signature string
	if err := fixture.database.DB().QueryRow(`SELECT signature_html FROM identities WHERE account_id = ? AND is_primary = 1`, created.ID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(signature), "script") || !strings.Contains(signature, "Regards") {
		t.Fatalf("unsafe signature stored during account creation: %q", signature)
	}

	update := fixture.request(t, http.MethodPatch, "/api/v1/accounts/"+created.ID, map[string]any{
		"display_name":   "Updated Mail",
		"color":          "#aabbcc",
		"signature_html": `<strong>Updated</strong><img src="https://tracker.example/pixel" onerror="bad()">`,
	}, true, true)
	assertStatus(t, update, http.StatusOK)
	var updated repository.AccountSummary
	decodeResponse(t, update, &updated)
	if updated.DisplayName != "Updated Mail" || updated.Color != "#AABBCC" {
		t.Fatalf("unexpected updated account: %+v", updated)
	}
	if err := fixture.database.DB().QueryRow(`SELECT signature_html FROM identities WHERE account_id = ? AND is_primary = 1`, created.ID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(signature), "onerror") || strings.Contains(signature, ` src="https://`) {
		t.Fatalf("unsafe updated signature stored: %q", signature)
	}
	fixture.runtime.mu.Lock()
	triggered = append([]string(nil), fixture.runtime.triggeredAccountIDs...)
	fixture.runtime.mu.Unlock()
	if len(triggered) != 1 {
		t.Fatalf("profile-only update unexpectedly triggered sync: %v", triggered)
	}

	connectionUpdate := fixture.request(t, http.MethodPatch, "/api/v1/accounts/"+created.ID, map[string]any{
		"username": "new-owner@example.com",
		"secret":   "",
		"imap":     map[string]any{"host": "imap.new.example.com", "port": 143, "tls_mode": "starttls"},
		"smtp":     map[string]any{"host": "smtp.new.example.com", "port": 587, "tls_mode": "starttls"},
	}, true, true)
	assertStatus(t, connectionUpdate, http.StatusOK)
	if strings.Contains(connectionUpdate.Body.String(), "app-password") {
		t.Fatal("connection update response leaked the retained secret")
	}
	decodeResponse(t, connectionUpdate, &updated)
	if updated.Status != "pending" || updated.Username != "new-owner@example.com" ||
		updated.IMAP.Host != "imap.new.example.com" || updated.IMAP.Port != 143 || updated.IMAP.TLSMode != "starttls" ||
		updated.SMTP.Host != "smtp.new.example.com" || updated.SMTP.Port != 587 || updated.SMTP.TLSMode != "starttls" {
		t.Fatalf("unexpected connection update response: %+v", updated)
	}
	record, err = fixture.repository.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err = fixture.cipher.Open(record.CredentialEncrypted, accountsecret.Purpose("imap", "owner@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := accountsecret.Unmarshal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != "new-owner@example.com" || credential.Secret != "app-password" || credential.IMAPTLSMode != accounts.TLSStartTLS || credential.SMTPTLSMode != accounts.TLSStartTLS {
		t.Fatalf("empty secret did not preserve the credential: %+v", credential)
	}
	fixture.runtime.mu.Lock()
	triggered = append([]string(nil), fixture.runtime.triggeredAccountIDs...)
	fixture.runtime.mu.Unlock()
	if len(triggered) != 2 || triggered[1] != created.ID {
		t.Fatalf("connection update sync triggers = %v", triggered)
	}

	secretUpdate := fixture.request(t, http.MethodPatch, "/api/v1/accounts/"+created.ID, map[string]any{
		"secret": "replacement-password",
	}, true, true)
	assertStatus(t, secretUpdate, http.StatusOK)
	if strings.Contains(secretUpdate.Body.String(), "replacement-password") {
		t.Fatal("connection update response leaked the replacement secret")
	}
	record, err = fixture.repository.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err = fixture.cipher.Open(record.CredentialEncrypted, accountsecret.Purpose("imap", "owner@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err = accountsecret.Unmarshal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "replacement-password" || credential.Username != "new-owner@example.com" {
		t.Fatalf("replacement credential was not persisted: %+v", credential)
	}

	list := fixture.request(t, http.MethodGet, "/api/v1/accounts", nil, true, false)
	assertStatus(t, list, http.StatusOK)
	if strings.Contains(list.Body.String(), "replacement-password") || strings.Contains(list.Body.String(), "app-password") {
		t.Fatal("account list leaked a credential secret")
	}
	var listed ListResponse[repository.AccountSummary]
	decodeResponse(t, list, &listed)
	if len(listed.Items) != 1 || listed.Items[0].Username != "new-owner@example.com" || listed.Items[0].IMAP.TLSMode != "starttls" {
		t.Fatalf("unexpected account list connection details: %+v", listed.Items)
	}
}

func TestHTTPOAuthReauthentication(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	configure := fixture.request(t, http.MethodPut, "/api/v1/settings/oauth/google", map[string]any{
		"client_id": "client-id", "client_secret": "client-secret",
	}, true, true)
	assertStatus(t, configure, http.StatusOK)

	preset, err := accounts.PresetFor(accounts.ProviderGoogle)
	if err != nil {
		t.Fatal(err)
	}
	oldCredential, err := accountsecret.MarshalOAuth(accountsecret.OAuthCredential{
		Username: "oauth@example.com", Token: &oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh"},
		IMAPTLSMode: preset.IMAP.TLSMode, SMTPTLSMode: preset.SMTP.TLSMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldEncrypted, err := fixture.cipher.Seal(oldCredential, accountsecret.Purpose("google", "oauth@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	existing, err := fixture.repository.CreateAccount(context.Background(), repository.AccountInput{
		DisplayName: "OAuth Mail", Email: "oauth@example.com", Provider: "google", Color: "#315B7D", AuthType: "oauth2",
		IMAPHost: preset.IMAP.Host, IMAPPort: int(preset.IMAP.Port), SMTPHost: preset.SMTP.Host, SMTPPort: int(preset.SMTP.Port),
		OAuthTokenEncrypted: oldEncrypted, SignatureHTML: "<p>OAuth signature</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.UpdateAccountStatus(context.Background(), existing.ID, "reauth_required", "oauth_refresh_failed"); err != nil {
		t.Fatal(err)
	}

	start := fixture.request(t, http.MethodPost, "/api/v1/oauth/google/start", map[string]any{
		"account_id": existing.ID,
	}, true, true)
	assertStatus(t, start, http.StatusOK)
	var begin oauthflow.BeginResult
	decodeResponse(t, start, &begin)
	authorizationURL, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")
	if state == "" {
		t.Fatalf("OAuth start response has no state: %s", begin.AuthorizationURL)
	}
	var pendingJSON string
	if err := fixture.database.DB().QueryRow(`SELECT pending_account_json FROM oauth_states WHERE used_at IS NULL ORDER BY created_at DESC LIMIT 1`).Scan(&pendingJSON); err != nil {
		t.Fatal(err)
	}
	var pending oauthflow.PendingAccount
	if err := json.Unmarshal([]byte(pendingJSON), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.AccountID != existing.ID || pending.Email != "oauth@example.com" || pending.DisplayName != "OAuth Mail" || pending.Color != "#315B7D" {
		t.Fatalf("OAuth state did not use canonical account data: %+v", pending)
	}

	providerMismatch := fixture.request(t, http.MethodPost, "/api/v1/oauth/microsoft/start", map[string]any{
		"account_id": existing.ID,
	}, true, true)
	assertAPIError(t, providerMismatch, http.StatusConflict, "account_mismatch")
	emailMismatch := fixture.request(t, http.MethodPost, "/api/v1/oauth/google/start", map[string]any{
		"account_id": existing.ID, "email": "other@example.com",
	}, true, true)
	assertAPIError(t, emailMismatch, http.StatusConflict, "account_mismatch")
	passwordCredential, err := accountsecret.Marshal(accountsecret.Credential{
		Username: "password@example.com", Secret: "password-secret",
		IMAPTLSMode: preset.IMAP.TLSMode, SMTPTLSMode: preset.SMTP.TLSMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	passwordEncrypted, err := fixture.cipher.Seal(passwordCredential, accountsecret.Purpose("google", "password@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	passwordAccount, err := fixture.repository.CreateAccount(context.Background(), repository.AccountInput{
		DisplayName: "Password Mail", Email: "password@example.com", Provider: "google", Color: "#31A36D", AuthType: "password",
		IMAPHost: preset.IMAP.Host, IMAPPort: int(preset.IMAP.Port), SMTPHost: preset.SMTP.Host, SMTPPort: int(preset.SMTP.Port),
		CredentialEncrypted: passwordEncrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	authMismatch := fixture.request(t, http.MethodPost, "/api/v1/oauth/google/start", map[string]any{
		"account_id": passwordAccount.ID,
	}, true, true)
	assertAPIError(t, authMismatch, http.StatusConflict, "account_mismatch")

	callbackRequest := httptest.NewRequest(http.MethodGet, testPublicURL+"/api/v1/oauth/google/callback?state="+url.QueryEscape(state)+"&code=auth-code", nil)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != preset.OAuth.TokenEndpoint {
			t.Fatalf("unexpected OAuth exchange endpoint: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)),
			Request:    request,
		}, nil
	})}
	callbackRequest = callbackRequest.WithContext(context.WithValue(callbackRequest.Context(), oauth2.HTTPClient, httpClient))
	callback := httptest.NewRecorder()
	fixture.handler.ServeHTTP(callback, callbackRequest)
	assertStatus(t, callback, http.StatusSeeOther)
	if location := callback.Header().Get("Location"); location != testPublicURL+"/settings/accounts?oauth=success" {
		t.Fatalf("unexpected OAuth callback redirect: %q", location)
	}

	accountsAfter, err := fixture.repository.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var oauthAccount repository.AccountSummary
	for _, account := range accountsAfter {
		if account.ID == existing.ID {
			oauthAccount = account
		}
	}
	if len(accountsAfter) != 2 || oauthAccount.ID != existing.ID || oauthAccount.Status != "pending" {
		t.Fatalf("OAuth callback created a duplicate account: %+v", accountsAfter)
	}
	record, err := fixture.repository.GetAccount(context.Background(), existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := fixture.cipher.Open(record.OAuthTokenEncrypted, accountsecret.Purpose("google", "oauth@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := accountsecret.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Token == nil || credential.Token.AccessToken != "new-access" || credential.Token.RefreshToken != "new-refresh" {
		t.Fatalf("OAuth token was not replaced: %+v", credential.Token)
	}
	list := fixture.request(t, http.MethodGet, "/api/v1/accounts", nil, true, false)
	assertStatus(t, list, http.StatusOK)
	if strings.Contains(list.Body.String(), "new-access") || strings.Contains(list.Body.String(), "new-refresh") || strings.Contains(list.Body.String(), "password-secret") {
		t.Fatal("account list leaked credential material")
	}
	var listed ListResponse[repository.AccountSummary]
	decodeResponse(t, list, &listed)
	var listedOAuth repository.AccountSummary
	for _, account := range listed.Items {
		if account.ID == existing.ID {
			listedOAuth = account
		}
	}
	if listedOAuth.Username != "oauth@example.com" || listedOAuth.IMAP.Host != preset.IMAP.Host || listedOAuth.IMAP.TLSMode != string(preset.IMAP.TLSMode) ||
		listedOAuth.SMTP.Host != preset.SMTP.Host || listedOAuth.SMTP.TLSMode != string(preset.SMTP.TLSMode) {
		t.Fatalf("OAuth account list omitted connection details: %+v", listedOAuth)
	}
	fixture.runtime.mu.Lock()
	triggered := append([]string(nil), fixture.runtime.triggeredAccountIDs...)
	fixture.runtime.mu.Unlock()
	if len(triggered) != 1 || triggered[0] != existing.ID {
		t.Fatalf("OAuth reauthentication sync triggers = %v", triggered)
	}
}

func TestHTTPDraftAutosaveAndSend(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	account := createAccountThroughAPI(t, fixture)
	create := fixture.request(t, http.MethodPost, "/api/v1/drafts", map[string]any{
		"account_id": account.ID,
		"to":         []map[string]string{{"email": "recipient@example.com"}},
		"subject":    "Initial subject",
		"body_text":  "Initial body",
		"body_html":  `<p>Initial body</p><script>alert("x")</script>`,
	}, true, true)
	assertStatus(t, create, http.StatusCreated)
	var draft repository.Draft
	decodeResponse(t, create, &draft)
	if draft.Version != 1 || strings.Contains(strings.ToLower(draft.BodyHTML), "script") {
		t.Fatalf("unexpected created draft: %+v", draft)
	}

	autosave := fixture.request(t, http.MethodPatch, "/api/v1/drafts/"+draft.ID, map[string]any{
		"account_id": account.ID,
		"to":         []map[string]string{{"email": "recipient@example.com"}},
		"subject":    "Autosaved subject",
		"body_text":  "Autosaved body",
		"body_html":  "<p>Autosaved body</p>",
	}, true, true)
	assertStatus(t, autosave, http.StatusOK)
	decodeResponse(t, autosave, &draft)
	if draft.Version != 2 || draft.Subject != "Autosaved subject" {
		t.Fatalf("unexpected autosaved draft: %+v", draft)
	}

	send := fixture.request(t, http.MethodPost, "/api/v1/drafts/"+draft.ID+"/send", map[string]any{}, true, true)
	assertStatus(t, send, http.StatusAccepted)
	var queued struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	decodeResponse(t, send, &queued)
	if queued.ID == "" || queued.State != "queued" {
		t.Fatalf("unexpected send result: %+v", queued)
	}
	var state, queuedDraftID string
	if err := fixture.database.DB().QueryRow(`SELECT state, draft_id FROM outbox WHERE id = ?`, queued.ID).Scan(&state, &queuedDraftID); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.mu.Lock()
	outboxWakes := fixture.runtime.outboxWakes
	fixture.runtime.mu.Unlock()
	if state != "queued" || queuedDraftID != draft.ID || outboxWakes != 1 {
		t.Fatalf("outbox state=%q draft=%q wakes=%d", state, queuedDraftID, outboxWakes)
	}
	statusResponse := fixture.request(t, http.MethodGet, "/api/v1/outbox/"+queued.ID, nil, true, false)
	assertStatus(t, statusResponse, http.StatusOK)
	var status repository.OutboxStatus
	decodeResponse(t, statusResponse, &status)
	if status.ID != queued.ID || status.DraftID != draft.ID || status.Status != "queued" {
		t.Fatalf("unexpected outbox status response: %+v", status)
	}
}

func TestHTTPOperationAndUndo(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	account := createAccountThroughAPI(t, fixture)
	seedMessage(t, fixture.database, account.ID, "conversation-operation", "message-operation", "<p>body</p>")

	create := fixture.request(t, http.MethodPost, "/api/v1/operations", map[string]any{
		"type":             "archive",
		"conversation_ids": []string{"conversation-operation"},
	}, true, true)
	assertStatus(t, create, http.StatusAccepted)
	var operation struct {
		ID           string     `json:"id"`
		Status       string     `json:"status"`
		OperationIDs []string   `json:"operation_ids"`
		UndoUntil    *time.Time `json:"undoable_until"`
	}
	decodeResponse(t, create, &operation)
	if operation.ID == "" || operation.Status != "pending" || len(operation.OperationIDs) != 1 || operation.UndoUntil == nil {
		t.Fatalf("unexpected operation response: %+v", operation)
	}
	fixture.runtime.mu.Lock()
	operationWakes := fixture.runtime.operationWakes
	fixture.runtime.mu.Unlock()
	if operationWakes != 1 {
		t.Fatalf("operation worker wakes = %d", operationWakes)
	}

	undo := fixture.request(t, http.MethodPost, "/api/v1/operations/"+operation.ID+"/undo", map[string]any{}, true, true)
	assertStatus(t, undo, http.StatusOK)
	var undoBody map[string]any
	decodeResponse(t, undo, &undoBody)
	if undoBody["status"] != "undone" {
		t.Fatalf("unexpected undo response: %+v", undoBody)
	}
	var status string
	if err := fixture.database.DB().QueryRow(`SELECT status FROM operations WHERE id = ?`, operation.OperationIDs[0]).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("operation status after undo = %q", status)
	}
	secondUndo := fixture.request(t, http.MethodPost, "/api/v1/operations/"+operation.ID+"/undo", map[string]any{}, true, true)
	assertAPIError(t, secondUndo, http.StatusConflict, "conflict")
}

func TestHTTPRemoteImagesAndAttachmentDownload(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	account := createAccountThroughAPI(t, fixture)
	clean, err := connectors.SanitizeHTML(`<p>Hello</p><img src="https://tracker.example/pixel" alt="remote">`, false)
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, fixture.database, account.ID, "conversation-body", "message-body", clean.HTML)

	allowed := fixture.request(t, http.MethodGet, "/api/v1/messages/message-body/body", nil, true, false)
	assertStatus(t, allowed, http.StatusOK)
	var allowedBody struct {
		HTML                string `json:"html"`
		RemoteImagesBlocked bool   `json:"remote_images_blocked"`
	}
	decodeResponse(t, allowed, &allowedBody)
	if allowedBody.RemoteImagesBlocked || !strings.Contains(allowedBody.HTML, `src="https://tracker.example/pixel"`) || strings.Contains(allowedBody.HTML, "data-mm-remote-src") {
		t.Fatalf("remote image was not allowed by default: %+v", allowedBody)
	}

	blocked := fixture.request(t, http.MethodGet, "/api/v1/messages/message-body/body?remote_images=block", nil, true, false)
	assertStatus(t, blocked, http.StatusOK)
	var blockedBody struct {
		HTML                string `json:"html"`
		RemoteImagesBlocked bool   `json:"remote_images_blocked"`
	}
	decodeResponse(t, blocked, &blockedBody)
	if !blockedBody.RemoteImagesBlocked || !strings.Contains(blockedBody.HTML, "data-mm-remote-src") || strings.Contains(blockedBody.HTML, ` src="https://`) {
		t.Fatalf("explicit remote image block was not applied: %+v", blockedBody)
	}

	attachmentPath := filepath.Join(t.TempDir(), "download.bin")
	attachmentBody := []byte("attachment-content")
	if err := os.WriteFile(attachmentPath, attachmentBody, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.attachmentPath = attachmentPath
	now := fixture.now.UnixMilli()
	execHTTPTestSQL(t, fixture.database, `
		INSERT INTO attachments (id, message_id, part_id, filename, content_type, disposition, size_bytes, created_at)
		VALUES ('attachment-download', 'message-body', '2', ?, 'not a media type', 'attachment', ?, ?)`,
		"../../report\r\n\"Q1\".html", len(attachmentBody), now)
	download := fixture.request(t, http.MethodGet, "/api/v1/attachments/attachment-download", nil, true, false)
	assertStatus(t, download, http.StatusOK)
	if !bytes.Equal(download.Body.Bytes(), attachmentBody) {
		t.Fatalf("downloaded attachment = %q", download.Body.Bytes())
	}
	if contentType := download.Header().Get("Content-Type"); contentType != "application/octet-stream" {
		t.Fatalf("attachment content type = %q", contentType)
	}
	disposition := download.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment; filename*=UTF-8''") || strings.ContainsAny(disposition, "\r\n") || strings.Contains(disposition, "..") {
		t.Fatalf("unsafe content disposition: %q", disposition)
	}
	if download.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("attachment response is missing nosniff")
	}
	fixture.runtime.mu.Lock()
	loads := fixture.runtime.attachmentLoads
	fixture.runtime.mu.Unlock()
	if loads != 1 {
		t.Fatalf("attachment loader calls = %d", loads)
	}
}

func TestHTTPFolderRoleConfirmation(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	account := createAccountThroughAPI(t, fixture)
	now := fixture.now.UnixMilli()
	execHTTPTestSQL(t, fixture.database, `
		INSERT INTO folders (id, account_id, remote_name, display_name, role, role_source, created_at, updated_at)
		VALUES ('folder-confirm', ?, 'Sent Custom', 'Sent Custom', 'other', 'needs_user', ?, ?)`, account.ID, now, now)

	response := fixture.request(t, http.MethodPatch, "/api/v1/folders/folder-confirm", map[string]any{"role": "sent"}, true, true)
	assertStatus(t, response, http.StatusOK)
	var mailbox repository.MailboxSummary
	decodeResponse(t, response, &mailbox)
	if mailbox.Role != "sent" || mailbox.RoleSource != "user" || mailbox.AccountID != account.ID {
		t.Fatalf("unexpected confirmed folder: %+v", mailbox)
	}
}

func validAccountPayload() map[string]any {
	return map[string]any{
		"display_name": "Personal Mail",
		"email":        "owner@example.com",
		"provider":     "imap",
		"color":        "#31A36D",
		"auth_type":    "password",
		"username":     "owner@example.com",
		"secret":       "app-password",
		"imap":         map[string]any{"host": "imap.example.com", "port": 993, "tls_mode": "implicit"},
		"smtp":         map[string]any{"host": "smtp.example.com", "port": 465, "tls_mode": "implicit"},
	}
}

func createAccountThroughAPI(t *testing.T, fixture *httpFixture) repository.AccountSummary {
	t.Helper()
	response := fixture.request(t, http.MethodPost, "/api/v1/accounts", validAccountPayload(), true, true)
	assertStatus(t, response, http.StatusCreated)
	var account repository.AccountSummary
	decodeResponse(t, response, &account)
	return account
}

func seedMessage(t *testing.T, database *store.Store, accountID, conversationID, messageID, bodyHTML string) {
	t.Helper()
	now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC).UnixMilli()
	folderID := "folder-" + conversationID
	execHTTPTestSQL(t, database, `
		INSERT INTO folders (id, account_id, remote_name, display_name, role, created_at, updated_at)
		VALUES (?, ?, ?, 'Inbox', 'inbox', ?, ?)`, folderID, accountID, folderID, now, now)
	execHTTPTestSQL(t, database, `
		INSERT INTO conversations (id, account_id, thread_key, subject, preview, latest_at, message_count, unread_count, starred, created_at, updated_at)
		VALUES (?, ?, ?, 'Subject', 'Preview', ?, 1, 1, 0, ?, ?)`, conversationID, accountID, "thread-"+conversationID, now, now, now)
	execHTTPTestSQL(t, database, `
		INSERT INTO messages (
			id, account_id, conversation_id, subject, from_json, to_json, cc_json, bcc_json, reply_to_json,
			received_at, preview, body_text, body_html_clean, seen, created_at, updated_at
		) VALUES (?, ?, ?, 'Subject', '[{"email":"sender@example.com"}]', '[]', '[]', '[]', '[]', ?, 'Preview', 'Plain body', ?, 0, ?, ?)`,
		messageID, accountID, conversationID, now, bodyHTML, now, now)
	execHTTPTestSQL(t, database, `
		INSERT INTO message_locations (id, message_id, account_id, folder_id, uid_validity, uid, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, 1, ?, ?)`, "location-"+messageID, messageID, accountID, folderID, now, now)
}

func execHTTPTestSQL(t *testing.T, database *store.Store, query string, args ...any) {
	t.Helper()
	if _, err := database.DB().Exec(query, args...); err != nil {
		t.Fatalf("execute test SQL: %v\n%s", err, query)
	}
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, want, response.Body.String())
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertStatus(t, response, status)
	var body ErrorBody
	decodeResponse(t, response, &body)
	if body.Error.Code != code || body.Error.RequestID == "" {
		t.Fatalf("unexpected API error: %+v", body.Error)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}
