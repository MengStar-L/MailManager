package accounts

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProviderPresetAndValidation(t *testing.T) {
	if ProviderCustom != "imap" {
		t.Fatalf("generic provider wire value = %q, want imap", ProviderCustom)
	}
	preset, err := PresetFor(ProviderGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if preset.IMAP.Host != "imap.gmail.com" || preset.SMTP.TLSMode != TLSStartTLS || preset.OAuth == nil {
		t.Fatalf("unexpected Google preset: %#v", preset)
	}
	config := Config{
		Provider: ProviderGoogle, AuthMethod: AuthOAuth2, IMAP: preset.IMAP, SMTP: preset.SMTP,
		Identity: Identity{Email: "person@example.com"}, Credentials: Credentials{Username: "person@example.com", Secret: "token"},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	config.IMAP.Host = " imap.gmail.com"
	if err := config.Validate(); err == nil {
		t.Fatal("host with surrounding whitespace was accepted")
	}
}

func TestOAuthPKCEStateIsSingleUse(t *testing.T) {
	now := time.Date(2026, time.July, 11, 1, 2, 3, 0, time.UTC)
	store := NewOAuthStateStore(10 * time.Minute)
	store.now = func() time.Time { return now }
	request, err := store.Begin(ProviderMicrosoft, "client-id", "https://mail.example.test/api/v1/oauth/microsoft/callback")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.State) < 32 || len(request.PKCEVerifier) < 43 || request.PKCEChallenge == request.PKCEVerifier {
		t.Fatalf("weak OAuth request material: %#v", request)
	}
	parsed, err := url.Parse(request.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("state") != request.State || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL is missing state or PKCE: %s", request.AuthorizationURL)
	}
	verifier, err := store.Consume(ProviderMicrosoft, request.State)
	if err != nil || verifier != request.PKCEVerifier {
		t.Fatalf("consume returned %q, %v", verifier, err)
	}
	if _, err := store.Consume(ProviderMicrosoft, request.State); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("replayed state was not rejected: %v", err)
	}
}

func TestOAuthStateExpires(t *testing.T) {
	now := time.Now()
	store := NewOAuthStateStore(time.Minute)
	store.now = func() time.Time { return now }
	request, err := store.Begin(ProviderGoogle, "client-id", "https://mail.example.test/callback")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := store.Consume(ProviderGoogle, request.State); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired state was not rejected: %v", err)
	}
}

func TestFolderRolePriorityAndSentBehavior(t *testing.T) {
	role, source := InferFolderRole(ProviderGoogle, "[Gmail]/Trash", []string{`\Sent`})
	if role != FolderSent || source != RoleFromSpecialUse {
		t.Fatalf("SPECIAL-USE did not take priority: %s %s", role, source)
	}
	role, source = InferFolderRole(ProviderGoogle, "[Gmail]/All Mail", nil)
	if role != FolderAll || source != RoleFromPreset {
		t.Fatalf("Gmail preset not detected: %s %s", role, source)
	}
	if !ProviderSavesSentCopy(ProviderGoogle) || ProviderSavesSentCopy(ProviderCustom) {
		t.Fatal("unexpected provider Sent-copy behavior")
	}
}

func TestCustomProviderCommonFolderPresets(t *testing.T) {
	tests := map[string]FolderRole{
		"Sent": FolderSent, "Sent Items": FolderSent, "Sent Messages": FolderSent,
		"Drafts": FolderDrafts, "Trash": FolderTrash, "Deleted Items": FolderTrash,
	}
	for name, expected := range tests {
		role, source := InferFolderRole(ProviderCustom, name, nil)
		if role != expected || source != RoleFromPreset {
			t.Errorf("InferFolderRole(imap, %q) = %q, %q; want %q, %q", name, role, source, expected, RoleFromPreset)
		}
	}
}
