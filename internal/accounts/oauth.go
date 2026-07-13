package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

type OAuthProvider struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	Scopes                []string
}

type OAuthRequest struct {
	Provider         Provider  `json:"provider"`
	State            string    `json:"state"`
	PKCEVerifier     string    `json:"-"`
	PKCEChallenge    string    `json:"-"`
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type oauthState struct {
	provider Provider
	verifier string
	expires  time.Time
}

type OAuthStateStore struct {
	mu     sync.Mutex
	ttl    time.Duration
	states map[string]oauthState
	now    func() time.Time
}

func NewOAuthStateStore(ttl time.Duration) *OAuthStateStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &OAuthStateStore{ttl: ttl, states: make(map[string]oauthState), now: time.Now}
}

func (s *OAuthStateStore) Begin(provider Provider, clientID, redirectURI string) (OAuthRequest, error) {
	preset, err := PresetFor(provider)
	if err != nil || preset.OAuth == nil {
		return OAuthRequest{}, fmt.Errorf("OAuth is not supported for provider %q", provider)
	}
	if strings.TrimSpace(clientID) == "" {
		return OAuthRequest{}, errors.New("OAuth client ID is required")
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.User != nil {
		return OAuthRequest{}, errors.New("OAuth redirect URI must be an absolute HTTPS URL")
	}
	state, err := randomURLToken(32)
	if err != nil {
		return OAuthRequest{}, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return OAuthRequest{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	expires := s.now().UTC().Add(s.ttl)

	authURL, err := url.Parse(preset.OAuth.AuthorizationEndpoint)
	if err != nil {
		return OAuthRequest{}, fmt.Errorf("invalid provider authorization endpoint: %w", err)
	}
	query := authURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirect.String())
	query.Set("scope", strings.Join(preset.OAuth.Scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("access_type", "offline")
	authURL.RawQuery = query.Encode()

	s.mu.Lock()
	s.pruneLocked(s.now())
	s.states[state] = oauthState{provider: provider, verifier: verifier, expires: expires}
	s.mu.Unlock()

	return OAuthRequest{
		Provider: provider, State: state, PKCEVerifier: verifier,
		PKCEChallenge: challenge, AuthorizationURL: authURL.String(), ExpiresAt: expires,
	}, nil
}

func (s *OAuthStateStore) Consume(provider Provider, receivedState string) (string, error) {
	if receivedState == "" {
		return "", errors.New("OAuth state is required")
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.states[receivedState]
	if ok {
		delete(s.states, receivedState)
	}
	if !ok {
		return "", errors.New("OAuth state is invalid or has already been used")
	}
	if now.After(stored.expires) {
		return "", errors.New("OAuth state has expired")
	}
	if stored.provider != provider {
		return "", errors.New("OAuth state belongs to a different provider")
	}
	return stored.verifier, nil
}

func (s *OAuthStateStore) pruneLocked(now time.Time) {
	for state, value := range s.states {
		if now.After(value.expires) {
			delete(s.states, state)
		}
	}
}

func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate secure random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
