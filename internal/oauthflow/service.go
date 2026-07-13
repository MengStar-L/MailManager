package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/store"
)

var (
	ErrNotConfigured = errors.New("OAuth provider is not configured")
	ErrInvalidState  = errors.New("OAuth state is invalid or expired")
)

type PendingAccount struct {
	AccountID     string `json:"account_id,omitempty"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	Color         string `json:"color"`
	SignatureHTML string `json:"signature_html,omitempty"`
}

type BeginResult struct {
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type ProviderStatus struct {
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
	ClientID   string `json:"client_id,omitempty"`
}

type ProviderConfig struct {
	ClientID     string
	ClientSecret string
}

type Service struct {
	store     *store.Store
	cipher    *cryptox.Cipher
	publicURL *url.URL
	now       func() time.Time
	ttl       time.Duration
	exchange  func(context.Context, *oauth2.Config, string, string) (*oauth2.Token, error)
}

func New(storeValue *store.Store, cipher *cryptox.Cipher, publicURL *url.URL) (*Service, error) {
	if storeValue == nil || cipher == nil || publicURL == nil || (publicURL.Scheme != "https" && publicURL.Scheme != "http") || publicURL.Host == "" {
		return nil, errors.New("OAuth requires a store, cipher, and absolute public URL")
	}
	return &Service{
		store: storeValue, cipher: cipher, publicURL: publicURL, now: func() time.Time { return time.Now().UTC() }, ttl: 10 * time.Minute,
		exchange: func(ctx context.Context, config *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
			return config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
		},
	}, nil
}

func (s *Service) ConfigureProvider(ctx context.Context, provider accounts.Provider, clientID, clientSecret string) error {
	if provider != accounts.ProviderGoogle && provider != accounts.ProviderMicrosoft {
		return errors.New("provider does not support OAuth")
	}
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" || clientSecret == "" || strings.ContainsAny(clientID+clientSecret, "\x00\r\n") {
		return errors.New("client ID and secret are required")
	}
	encrypted, err := s.cipher.Seal([]byte(clientSecret), providerSecretPurpose(provider))
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	return s.store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO provider_oauth_configs (provider, client_id, client_secret_encrypted, configured_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(provider) DO UPDATE SET client_id = excluded.client_id,
			    client_secret_encrypted = excluded.client_secret_encrypted, updated_at = excluded.updated_at`,
			provider, clientID, encrypted, now, now)
		return err
	})
}

func (s *Service) ProviderStatuses(ctx context.Context) ([]ProviderStatus, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT provider, client_id FROM provider_oauth_configs ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configured := make(map[string]string)
	for rows.Next() {
		var provider, clientID string
		if err := rows.Scan(&provider, &clientID); err != nil {
			return nil, err
		}
		configured[provider] = clientID
	}
	statuses := make([]ProviderStatus, 0, 2)
	for _, provider := range []string{string(accounts.ProviderGoogle), string(accounts.ProviderMicrosoft)} {
		clientID, ok := configured[provider]
		statuses = append(statuses, ProviderStatus{Provider: provider, Configured: ok, ClientID: clientID})
	}
	return statuses, rows.Err()
}

func (s *Service) Begin(ctx context.Context, provider accounts.Provider, pending PendingAccount) (BeginResult, error) {
	providerConfig, err := s.providerConfig(ctx, provider)
	if err != nil {
		return BeginResult{}, err
	}
	preset, err := accounts.PresetFor(provider)
	if err != nil || preset.OAuth == nil {
		return BeginResult{}, errors.New("provider does not support OAuth")
	}
	state, err := randomToken(32)
	if err != nil {
		return BeginResult{}, err
	}
	verifier, err := randomToken(64)
	if err != nil {
		return BeginResult{}, err
	}
	id, err := store.NewID()
	if err != nil {
		return BeginResult{}, err
	}
	redirectURI := s.redirectURI(provider)
	encryptedVerifier, err := s.cipher.Seal([]byte(verifier), stateVerifierPurpose(id))
	if err != nil {
		return BeginResult{}, err
	}
	pendingJSON, err := json.Marshal(pending)
	if err != nil {
		return BeginResult{}, err
	}
	expiresAt := s.now().Add(s.ttl)
	hash := sha256.Sum256([]byte(state))
	if err := s.store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_states (id, provider, state_hash, pkce_verifier_encrypted, redirect_uri, pending_account_json, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, provider, hash[:], encryptedVerifier, redirectURI, string(pendingJSON), expiresAt.UnixMilli(), s.now().UnixMilli())
		return err
	}); err != nil {
		return BeginResult{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	config := oauth2.Config{
		ClientID: providerConfig.ClientID, ClientSecret: providerConfig.ClientSecret,
		Endpoint:    oauth2.Endpoint{AuthURL: preset.OAuth.AuthorizationEndpoint, TokenURL: preset.OAuth.TokenEndpoint},
		RedirectURL: redirectURI, Scopes: preset.OAuth.Scopes,
	}
	options := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(digest[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if provider == accounts.ProviderGoogle {
		options = append(options, oauth2.SetAuthURLParam("prompt", "consent"))
	}
	return BeginResult{AuthorizationURL: config.AuthCodeURL(state, options...), ExpiresAt: expiresAt}, nil
}

func (s *Service) Exchange(ctx context.Context, provider accounts.Provider, state, code string) (PendingAccount, *oauth2.Token, error) {
	if state == "" || code == "" {
		return PendingAccount{}, nil, ErrInvalidState
	}
	hash := sha256.Sum256([]byte(state))
	var id, storedProvider, redirectURI, pendingJSON string
	var verifierEncrypted []byte
	err := s.store.WriteTx(ctx, func(tx *sql.Tx) error {
		var expiresAt int64
		err := tx.QueryRowContext(ctx, `
			SELECT id, provider, pkce_verifier_encrypted, redirect_uri, pending_account_json, expires_at
			FROM oauth_states WHERE state_hash = ? AND used_at IS NULL`, hash[:]).Scan(
			&id, &storedProvider, &verifierEncrypted, &redirectURI, &pendingJSON, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (storedProvider != string(provider) || expiresAt <= s.now().UnixMilli()) {
			return ErrInvalidState
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE oauth_states SET used_at = ? WHERE id = ? AND used_at IS NULL`, s.now().UnixMilli(), id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrInvalidState
		}
		return nil
	})
	if err != nil {
		return PendingAccount{}, nil, err
	}
	verifier, err := s.cipher.Open(verifierEncrypted, stateVerifierPurpose(id))
	if err != nil {
		return PendingAccount{}, nil, ErrInvalidState
	}
	providerConfig, err := s.providerConfig(ctx, provider)
	if err != nil {
		return PendingAccount{}, nil, err
	}
	preset, err := accounts.PresetFor(provider)
	if err != nil || preset.OAuth == nil {
		return PendingAccount{}, nil, errors.New("provider does not support OAuth")
	}
	config := &oauth2.Config{
		ClientID: providerConfig.ClientID, ClientSecret: providerConfig.ClientSecret,
		Endpoint:    oauth2.Endpoint{AuthURL: preset.OAuth.AuthorizationEndpoint, TokenURL: preset.OAuth.TokenEndpoint},
		RedirectURL: redirectURI, Scopes: preset.OAuth.Scopes,
	}
	token, err := s.exchange(ctx, config, code, string(verifier))
	if err != nil {
		return PendingAccount{}, nil, fmt.Errorf("exchange OAuth code: %w", err)
	}
	var pending PendingAccount
	if err := json.Unmarshal([]byte(pendingJSON), &pending); err != nil {
		return PendingAccount{}, nil, ErrInvalidState
	}
	return pending, token, nil
}

func (s *Service) RefreshToken(ctx context.Context, provider accounts.Provider, token *oauth2.Token) (*oauth2.Token, error) {
	if token == nil || token.RefreshToken == "" {
		return nil, errors.New("OAuth refresh token is unavailable")
	}
	providerConfig, err := s.providerConfig(ctx, provider)
	if err != nil {
		return nil, err
	}
	preset, err := accounts.PresetFor(provider)
	if err != nil || preset.OAuth == nil {
		return nil, errors.New("provider does not support OAuth")
	}
	config := &oauth2.Config{
		ClientID: providerConfig.ClientID, ClientSecret: providerConfig.ClientSecret,
		Endpoint:    oauth2.Endpoint{AuthURL: preset.OAuth.AuthorizationEndpoint, TokenURL: preset.OAuth.TokenEndpoint},
		RedirectURL: s.redirectURI(provider), Scopes: preset.OAuth.Scopes,
	}
	refreshed, err := config.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh OAuth token: %w", err)
	}
	return refreshed, nil
}

func (s *Service) providerConfig(ctx context.Context, provider accounts.Provider) (ProviderConfig, error) {
	var config ProviderConfig
	var encrypted []byte
	err := s.store.DB().QueryRowContext(ctx, `
		SELECT client_id, client_secret_encrypted FROM provider_oauth_configs WHERE provider = ?`, provider).Scan(&config.ClientID, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderConfig{}, ErrNotConfigured
	}
	if err != nil {
		return ProviderConfig{}, err
	}
	secret, err := s.cipher.Open(encrypted, providerSecretPurpose(provider))
	if err != nil {
		return ProviderConfig{}, err
	}
	config.ClientSecret = string(secret)
	return config, nil
}

func (s *Service) redirectURI(provider accounts.Provider) string {
	base := *s.publicURL
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/oauth/" + string(provider) + "/callback"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func providerSecretPurpose(provider accounts.Provider) string {
	return "oauth-provider/" + string(provider) + "/client-secret"
}

func stateVerifierPurpose(id string) string {
	return "oauth-state/" + id + "/pkce-verifier"
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
