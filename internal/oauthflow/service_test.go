package oauthflow

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/store"
)

func TestOAuthStateIsPersistentAndSingleUse(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptox.NewCipher(make([]byte, cryptox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	publicURL, _ := url.Parse("https://mail.example.com")
	service, err := New(database, cipher, publicURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureProvider(ctx, accounts.ProviderGoogle, "client-id", "client-secret"); err != nil {
		t.Fatal(err)
	}
	service.exchange = func(_ context.Context, config *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
		if config.ClientID != "client-id" || code != "auth-code" || verifier == "" {
			t.Fatalf("unexpected exchange values: %#v %q %q", config, code, verifier)
		}
		return &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}, nil
	}
	pending := PendingAccount{AccountID: "01900000-0000-7000-8000-000000000001", DisplayName: "Personal", Email: "person@example.com", Color: "#315B7D"}
	begin, err := service.Begin(ctx, accounts.ProviderGoogle, pending)
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")
	if state == "" || authorizationURL.Query().Get("code_challenge") == "" {
		t.Fatalf("missing OAuth protections: %s", begin.AuthorizationURL)
	}
	gotPending, token, err := service.Exchange(ctx, accounts.ProviderGoogle, state, "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if gotPending.AccountID != pending.AccountID || gotPending.Email != pending.Email || token.RefreshToken != "refresh" {
		t.Fatalf("unexpected exchange result: %#v %#v", gotPending, token)
	}
	if _, _, err := service.Exchange(ctx, accounts.ProviderGoogle, state, "auth-code"); err != ErrInvalidState {
		t.Fatalf("expected replay rejection, got %v", err)
	}
}
