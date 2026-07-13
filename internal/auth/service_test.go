package auth

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mailmanager/internal/cryptox"
	"mailmanager/internal/store"
)

func TestServiceSetupLoginCSRFAndRecovery(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptox.NewCipher(bytes.Repeat([]byte{9}, cryptox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	service, err := NewService(db, cipher, Options{
		Now: func() time.Time { return now }, PasswordParams: testPasswordParams(),
		RateLimiter: NewRateLimiter(RateLimiterOptions{MaxFailures: 2, Window: time.Minute, Now: func() time.Time { return now }}),
	})
	if err != nil {
		t.Fatal(err)
	}

	bootstrap, err := service.IssueBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.BeginSetup(ctx, bootstrap.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := TOTPCode(enrollment.Secret, now)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CompleteSetup(ctx, bootstrap.Token, "a strong password", code)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Username != defaultUsername || len(setup.RecoveryCodes) != 10 {
		t.Fatalf("unexpected setup result: %+v", setup)
	}
	if _, err := service.IssueBootstrapToken(ctx); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second bootstrap error = %v", err)
	}

	for i := 0; i < 2; i++ {
		_, err = service.LoginPassword(ctx, PasswordLoginInput{Password: "wrong password", ClientKey: "198.51.100.1"})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password error = %v", err)
		}
	}
	if _, err := service.LoginPassword(ctx, PasswordLoginInput{Password: "a strong password", ClientKey: "198.51.100.1"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}

	challenge, err := service.LoginPassword(ctx, PasswordLoginInput{Password: "a strong password", ClientKey: "198.51.100.2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoginTOTP(ctx, challenge.ChallengeToken, "000000"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("invalid TOTP error = %v", err)
	}
	code, _ = TOTPCode(enrollment.Secret, now)
	credentials, err := service.LoginTOTP(ctx, challenge.ChallengeToken, code)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(ctx, credentials.SessionToken)
	if err != nil || principal.AdminID != setup.AdminID {
		t.Fatalf("principal = %+v, %v", principal, err)
	}
	if err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken, "different"); !errors.Is(err, ErrCSRFInvalid) {
		t.Fatalf("bad CSRF error = %v", err)
	}

	recovery, err := service.Recover(ctx, RecoveryInput{
		RecoveryCode: setup.RecoveryCodes[0], NewPassword: "a newer password", ResetTOTP: true,
	})
	if err != nil || recovery.TOTPSecret == "" {
		t.Fatalf("recovery = %+v, %v", recovery, err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("recovery must revoke sessions, got %v", err)
	}
	if _, err := service.Recover(ctx, RecoveryInput{RecoveryCode: setup.RecoveryCodes[0], NewPassword: "another password"}); !errors.Is(err, ErrRecoveryCodeInvalid) {
		t.Fatalf("reused recovery code error = %v", err)
	}
	if _, err := service.LoginPassword(ctx, PasswordLoginInput{Password: "a strong password", ClientKey: "old-password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password error = %v", err)
	}
	if _, err := service.LoginPassword(ctx, PasswordLoginInput{Password: "a newer password", ClientKey: "new-password"}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

func TestSetupTokenExpires(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "expiry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, _ := cryptox.NewCipher(bytes.Repeat([]byte{4}, cryptox.KeySize))
	now := time.Unix(1000, 0).UTC()
	service, err := NewService(db, cipher, Options{Now: func() time.Time { return now }, SetupTTL: 30 * time.Minute, PasswordParams: testPasswordParams()})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := service.IssueBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if _, err := service.BeginSetup(ctx, bootstrap.Token, "admin"); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatalf("expired setup token error = %v", err)
	}
}
