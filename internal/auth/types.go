package auth

import (
	"context"
	"time"

	"mailmanager/internal/store"
)

type SetupStatus struct {
	Configured      bool       `json:"configured"`
	TokenExpiresAt  *time.Time `json:"token_expires_at,omitempty"`
	EnrollmentReady bool       `json:"enrollment_ready"`
}

type BootstrapToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SetupEnrollment struct {
	Secret          string    `json:"secret"`
	ProvisioningURI string    `json:"provisioning_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type SetupResult struct {
	AdminID       string   `json:"admin_id"`
	Username      string   `json:"username"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type PasswordLoginInput struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	ClientKey string `json:"-"`
}

type PasswordChallenge struct {
	ChallengeToken string    `json:"challenge_token"`
	ExpiresAt      time.Time `json:"expires_at"`
	TOTPRequired   bool      `json:"totp_required"`
}

type SessionCredentials struct {
	SessionToken string    `json:"-"`
	CSRFToken    string    `json:"csrf_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Principal struct {
	AdminID   string    `json:"admin_id"`
	Username  string    `json:"username"`
	SessionID string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RecoveryInput struct {
	RecoveryCode string
	NewPassword  string
	ResetTOTP    bool
}

type RecoveryResult struct {
	TOTPSecret      string `json:"totp_secret,omitempty"`
	ProvisioningURI string `json:"provisioning_uri,omitempty"`
}

// Manager is the complete single-admin authentication boundary consumed by
// the HTTP layer and the local recovery CLI.
type Manager interface {
	SetupStatus(context.Context) (SetupStatus, error)
	IssueBootstrapToken(context.Context) (BootstrapToken, error)
	BeginSetup(context.Context, string, string) (SetupEnrollment, error)
	CompleteSetup(context.Context, string, string, string) (SetupResult, error)
	LoginPassword(context.Context, PasswordLoginInput) (PasswordChallenge, error)
	LoginTOTP(context.Context, string, string) (SessionCredentials, error)
	Authenticate(context.Context, string) (Principal, error)
	ValidateCSRF(context.Context, string, string, string) error
	Logout(context.Context, string) error
	Recover(context.Context, RecoveryInput) (RecoveryResult, error)
}

// Repository is implemented by store.Store. Keeping it explicit makes the
// security service testable without exposing SQL to HTTP handlers.
type Repository interface {
	CountAdmins(context.Context) (int, error)
	ReplaceSetupToken(context.Context, store.SetupToken) error
	ActiveSetupToken(context.Context, time.Time) (store.SetupToken, error)
	SetupTokenByHash(context.Context, []byte) (store.SetupToken, error)
	SetSetupEnrollment(context.Context, string, string, []byte, time.Time) error
	CompleteSetup(context.Context, string, store.Admin, []store.RecoveryCode, time.Time) error
	AdminByUsername(context.Context, string) (store.Admin, error)
	AdminByID(context.Context, string) (store.Admin, error)
	CreateAuthChallenge(context.Context, store.AuthChallenge) error
	AuthChallengeByHash(context.Context, []byte) (store.AuthChallenge, error)
	IncrementAuthChallengeAttempts(context.Context, string) error
	ConsumeChallengeAndCreateSession(context.Context, string, int, store.Session, time.Time) error
	SessionByTokenHash(context.Context, []byte) (store.Session, error)
	DeleteSessionByTokenHash(context.Context, []byte) error
	RecoveryCodeByHash(context.Context, []byte) (store.RecoveryCode, error)
	ApplyRecovery(context.Context, string, string, string, []byte, time.Time) error
	PruneExpiredAuth(context.Context, time.Time) error
}

type SecretCipher interface {
	Seal([]byte, string) ([]byte, error)
	Open([]byte, string) ([]byte, error)
}
