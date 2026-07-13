package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mailmanager/internal/store"
)

const defaultUsername = "admin"

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Options struct {
	Issuer               string
	SetupTTL             time.Duration
	ChallengeTTL         time.Duration
	SessionTTL           time.Duration
	MaxChallengeAttempts int
	RecoveryCodeCount    int
	PasswordParams       PasswordParams
	RateLimiter          *RateLimiter
	Now                  func() time.Time
}

type Service struct {
	repository           Repository
	cipher               SecretCipher
	hasher               *PasswordHasher
	dummyPasswordHash    string
	issuer               string
	setupTTL             time.Duration
	challengeTTL         time.Duration
	sessionTTL           time.Duration
	maxChallengeAttempts int
	recoveryCodeCount    int
	limiter              *RateLimiter
	now                  func() time.Time
}

func NewService(repository Repository, cipher SecretCipher, options Options) (*Service, error) {
	if repository == nil || cipher == nil {
		return nil, errors.New("auth repository and secret cipher are required")
	}
	if options.Issuer == "" {
		options.Issuer = "MailManager"
	}
	if options.SetupTTL <= 0 {
		options.SetupTTL = 30 * time.Minute
	}
	if options.ChallengeTTL <= 0 {
		options.ChallengeTTL = 5 * time.Minute
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = 30 * 24 * time.Hour
	}
	if options.MaxChallengeAttempts <= 0 {
		options.MaxChallengeAttempts = 5
	}
	if options.RecoveryCodeCount <= 0 {
		options.RecoveryCodeCount = 10
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.PasswordParams == (PasswordParams{}) {
		options.PasswordParams = DefaultPasswordParams()
	}
	hasher, err := NewPasswordHasher(options.PasswordParams)
	if err != nil {
		return nil, fmt.Errorf("configure password hasher: %w", err)
	}
	dummyHash, err := hasher.Hash("not-a-real-mailmanager-password")
	if err != nil {
		return nil, err
	}
	if options.RateLimiter == nil {
		options.RateLimiter = NewRateLimiter(RateLimiterOptions{Now: options.Now})
	}
	return &Service{
		repository: repository, cipher: cipher, hasher: hasher, dummyPasswordHash: dummyHash,
		issuer: options.Issuer, setupTTL: options.SetupTTL, challengeTTL: options.ChallengeTTL,
		sessionTTL: options.SessionTTL, maxChallengeAttempts: options.MaxChallengeAttempts,
		recoveryCodeCount: options.RecoveryCodeCount, limiter: options.RateLimiter, now: options.Now,
	}, nil
}

func (s *Service) SetupStatus(ctx context.Context) (SetupStatus, error) {
	count, err := s.repository.CountAdmins(ctx)
	if err != nil {
		return SetupStatus{}, err
	}
	if count > 0 {
		return SetupStatus{Configured: true}, nil
	}
	token, err := s.repository.ActiveSetupToken(ctx, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return SetupStatus{}, nil
	}
	if err != nil {
		return SetupStatus{}, err
	}
	return SetupStatus{TokenExpiresAt: &token.ExpiresAt, EnrollmentReady: len(token.TOTPSecretEncrypted) > 0}, nil
}

// IssueBootstrapToken is intended to be called by the local process at startup
// and printed once to its protected terminal/journal. Only its digest is stored.
func (s *Service) IssueBootstrapToken(ctx context.Context) (BootstrapToken, error) {
	count, err := s.repository.CountAdmins(ctx)
	if err != nil {
		return BootstrapToken{}, err
	}
	if count > 0 {
		return BootstrapToken{}, ErrAlreadyConfigured
	}
	token, err := GenerateOpaqueToken()
	if err != nil {
		return BootstrapToken{}, err
	}
	id, err := store.NewID()
	if err != nil {
		return BootstrapToken{}, err
	}
	now := s.now()
	expiresAt := now.Add(s.setupTTL)
	if err := s.repository.ReplaceSetupToken(ctx, store.SetupToken{
		ID: id, TokenHash: HashOpaqueToken(token), ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return BootstrapToken{}, err
	}
	return BootstrapToken{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) BeginSetup(ctx context.Context, bootstrapToken, username string) (SetupEnrollment, error) {
	token, err := s.validSetupToken(ctx, bootstrapToken)
	if err != nil {
		return SetupEnrollment{}, err
	}
	username, err = normalizeUsername(username)
	if err != nil {
		return SetupEnrollment{}, err
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return SetupEnrollment{}, err
	}
	encrypted, err := s.cipher.Seal([]byte(secret), setupTOTPPurpose(token.ID))
	if err != nil {
		return SetupEnrollment{}, fmt.Errorf("encrypt setup TOTP secret: %w", err)
	}
	if err := s.repository.SetSetupEnrollment(ctx, token.ID, username, encrypted, s.now()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return SetupEnrollment{}, ErrSetupTokenInvalid
		}
		return SetupEnrollment{}, err
	}
	uri, err := TOTPProvisioningURI(s.issuer, username, secret)
	if err != nil {
		return SetupEnrollment{}, err
	}
	return SetupEnrollment{Secret: secret, ProvisioningURI: uri, ExpiresAt: token.ExpiresAt}, nil
}

func (s *Service) CompleteSetup(ctx context.Context, bootstrapToken, password, totpCode string) (SetupResult, error) {
	token, err := s.validSetupToken(ctx, bootstrapToken)
	if err != nil {
		return SetupResult{}, err
	}
	if token.Username == "" || len(token.TOTPSecretEncrypted) == 0 {
		return SetupResult{}, ErrEnrollmentRequired
	}
	if err := validateNewPassword(password); err != nil {
		return SetupResult{}, err
	}
	secretBytes, err := s.cipher.Open(token.TOTPSecretEncrypted, setupTOTPPurpose(token.ID))
	if err != nil {
		return SetupResult{}, fmt.Errorf("decrypt setup TOTP secret: %w", err)
	}
	if !VerifyTOTP(string(secretBytes), totpCode, s.now()) {
		return SetupResult{}, ErrInvalidTOTP
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return SetupResult{}, err
	}
	adminID, err := store.NewID()
	if err != nil {
		return SetupResult{}, err
	}
	encryptedTOTP, err := s.cipher.Seal(secretBytes, adminTOTPPurpose(adminID))
	if err != nil {
		return SetupResult{}, fmt.Errorf("encrypt admin TOTP secret: %w", err)
	}
	recoveryCodes, err := GenerateRecoveryCodes(s.recoveryCodeCount)
	if err != nil {
		return SetupResult{}, err
	}
	now := s.now()
	storedCodes := make([]store.RecoveryCode, len(recoveryCodes))
	for i, code := range recoveryCodes {
		id, err := store.NewID()
		if err != nil {
			return SetupResult{}, err
		}
		storedCodes[i] = store.RecoveryCode{ID: id, AdminID: adminID, CodeHash: HashRecoveryCode(code), CreatedAt: now}
	}
	admin := store.Admin{ID: adminID, Username: token.Username, PasswordHash: passwordHash,
		TOTPSecretEncrypted: encryptedTOTP, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CompleteSetup(ctx, token.ID, admin, storedCodes, now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return SetupResult{}, ErrSetupTokenInvalid
		}
		return SetupResult{}, err
	}
	return SetupResult{AdminID: adminID, Username: token.Username, RecoveryCodes: recoveryCodes}, nil
}

func (s *Service) LoginPassword(ctx context.Context, input PasswordLoginInput) (PasswordChallenge, error) {
	username, err := normalizeUsername(input.Username)
	if err != nil {
		username = defaultUsername
	}
	key := strings.ToLower(username) + "\x00" + strings.TrimSpace(input.ClientKey)
	if allowed, retry := s.limiter.Allow(key); !allowed {
		return PasswordChallenge{}, &RateLimitError{RetryAfter: retry}
	}

	admin, findErr := s.repository.AdminByUsername(ctx, username)
	hash := s.dummyPasswordHash
	if findErr == nil {
		hash = admin.PasswordHash
	} else if !errors.Is(findErr, store.ErrNotFound) {
		return PasswordChallenge{}, findErr
	}
	valid, verifyErr := s.hasher.Verify(hash, input.Password)
	if verifyErr != nil {
		valid = false
	}
	if findErr != nil || !valid {
		s.limiter.RecordFailure(key)
		return PasswordChallenge{}, ErrInvalidCredentials
	}
	s.limiter.Reset(key)

	token, err := GenerateOpaqueToken()
	if err != nil {
		return PasswordChallenge{}, err
	}
	id, err := store.NewID()
	if err != nil {
		return PasswordChallenge{}, err
	}
	now := s.now()
	expiresAt := now.Add(s.challengeTTL)
	if err := s.repository.CreateAuthChallenge(ctx, store.AuthChallenge{
		ID: id, AdminID: admin.ID, TokenHash: HashOpaqueToken(token), ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return PasswordChallenge{}, err
	}
	return PasswordChallenge{ChallengeToken: token, ExpiresAt: expiresAt, TOTPRequired: true}, nil
}

func (s *Service) LoginTOTP(ctx context.Context, challengeToken, code string) (SessionCredentials, error) {
	challenge, err := s.repository.AuthChallengeByHash(ctx, HashOpaqueToken(challengeToken))
	now := s.now()
	if errors.Is(err, store.ErrNotFound) {
		return SessionCredentials{}, ErrChallengeInvalid
	}
	if err != nil {
		return SessionCredentials{}, err
	}
	if challenge.UsedAt != nil || !challenge.ExpiresAt.After(now) || challenge.Attempts >= s.maxChallengeAttempts {
		return SessionCredentials{}, ErrChallengeInvalid
	}
	admin, err := s.repository.AdminByID(ctx, challenge.AdminID)
	if err != nil {
		return SessionCredentials{}, err
	}
	secret, err := s.cipher.Open(admin.TOTPSecretEncrypted, adminTOTPPurpose(admin.ID))
	if err != nil {
		return SessionCredentials{}, fmt.Errorf("decrypt admin TOTP secret: %w", err)
	}
	if !VerifyTOTP(string(secret), code, now) {
		if err := s.repository.IncrementAuthChallengeAttempts(ctx, challenge.ID); err != nil && !errors.Is(err, store.ErrConflict) {
			return SessionCredentials{}, err
		}
		return SessionCredentials{}, ErrInvalidTOTP
	}

	sessionToken, err := GenerateOpaqueToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	csrfToken, err := GenerateOpaqueToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	sessionID, err := store.NewID()
	if err != nil {
		return SessionCredentials{}, err
	}
	expiresAt := now.Add(s.sessionTTL)
	session := store.Session{ID: sessionID, AdminID: admin.ID, TokenHash: HashOpaqueToken(sessionToken),
		CSRFHash: HashOpaqueToken(csrfToken), ExpiresAt: expiresAt, LastSeenAt: now, CreatedAt: now}
	if err := s.repository.ConsumeChallengeAndCreateSession(ctx, challenge.ID, s.maxChallengeAttempts, session, now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return SessionCredentials{}, ErrChallengeInvalid
		}
		return SessionCredentials{}, err
	}
	return SessionCredentials{SessionToken: sessionToken, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, sessionToken string) (Principal, error) {
	if sessionToken == "" {
		return Principal{}, ErrSessionInvalid
	}
	session, err := s.repository.SessionByTokenHash(ctx, HashOpaqueToken(sessionToken))
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, ErrSessionInvalid
	}
	if err != nil {
		return Principal{}, err
	}
	if !session.ExpiresAt.After(s.now()) {
		_ = s.repository.DeleteSessionByTokenHash(ctx, session.TokenHash)
		return Principal{}, ErrSessionInvalid
	}
	return Principal{AdminID: session.AdminID, Username: session.Admin.Username, SessionID: session.ID, ExpiresAt: session.ExpiresAt}, nil
}

func (s *Service) ValidateCSRF(ctx context.Context, sessionToken, cookieToken, headerToken string) error {
	if sessionToken == "" || cookieToken == "" || headerToken == "" ||
		subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
		return ErrCSRFInvalid
	}
	session, err := s.repository.SessionByTokenHash(ctx, HashOpaqueToken(sessionToken))
	if err != nil || !session.ExpiresAt.After(s.now()) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return ErrSessionInvalid
	}
	if subtle.ConstantTimeCompare(session.CSRFHash, HashOpaqueToken(headerToken)) != 1 {
		return ErrCSRFInvalid
	}
	return nil
}

func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return nil
	}
	return s.repository.DeleteSessionByTokenHash(ctx, HashOpaqueToken(sessionToken))
}

// Recover is deliberately intended for the local CLI only. HTTP handlers
// should not expose it. Every successful recovery consumes one code and
// revokes all existing sessions.
func (s *Service) Recover(ctx context.Context, input RecoveryInput) (RecoveryResult, error) {
	if input.NewPassword == "" && !input.ResetTOTP {
		return RecoveryResult{}, errors.New("recovery must reset the password or TOTP")
	}
	code, err := s.repository.RecoveryCodeByHash(ctx, HashRecoveryCode(input.RecoveryCode))
	if errors.Is(err, store.ErrNotFound) || (err == nil && code.UsedAt != nil) {
		return RecoveryResult{}, ErrRecoveryCodeInvalid
	}
	if err != nil {
		return RecoveryResult{}, err
	}
	admin, err := s.repository.AdminByID(ctx, code.AdminID)
	if err != nil {
		return RecoveryResult{}, err
	}
	var passwordHash string
	if input.NewPassword != "" {
		if err := validateNewPassword(input.NewPassword); err != nil {
			return RecoveryResult{}, err
		}
		passwordHash, err = s.hasher.Hash(input.NewPassword)
		if err != nil {
			return RecoveryResult{}, err
		}
	}
	var encryptedTOTP []byte
	result := RecoveryResult{}
	if input.ResetTOTP {
		result.TOTPSecret, err = GenerateTOTPSecret()
		if err != nil {
			return RecoveryResult{}, err
		}
		result.ProvisioningURI, err = TOTPProvisioningURI(s.issuer, admin.Username, result.TOTPSecret)
		if err != nil {
			return RecoveryResult{}, err
		}
		encryptedTOTP, err = s.cipher.Seal([]byte(result.TOTPSecret), adminTOTPPurpose(admin.ID))
		if err != nil {
			return RecoveryResult{}, err
		}
	}
	if err := s.repository.ApplyRecovery(ctx, code.ID, admin.ID, passwordHash, encryptedTOTP, s.now()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return RecoveryResult{}, ErrRecoveryCodeInvalid
		}
		return RecoveryResult{}, err
	}
	return result, nil
}

func (s *Service) Prune(ctx context.Context) error {
	return s.repository.PruneExpiredAuth(ctx, s.now())
}

func (s *Service) validSetupToken(ctx context.Context, raw string) (store.SetupToken, error) {
	if raw == "" {
		return store.SetupToken{}, ErrSetupTokenInvalid
	}
	token, err := s.repository.SetupTokenByHash(ctx, HashOpaqueToken(raw))
	if errors.Is(err, store.ErrNotFound) {
		return store.SetupToken{}, ErrSetupTokenInvalid
	}
	if err != nil {
		return store.SetupToken{}, err
	}
	if token.UsedAt != nil || !token.ExpiresAt.After(s.now()) {
		return store.SetupToken{}, ErrSetupTokenInvalid
	}
	return token, nil
}

func normalizeUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		username = defaultUsername
	}
	if !usernamePattern.MatchString(username) {
		return "", errors.New("username must contain only letters, numbers, dot, underscore, or hyphen")
	}
	return username, nil
}

func validateNewPassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 12 || length > 1024 {
		return ErrWeakPassword
	}
	return nil
}

func setupTOTPPurpose(id string) string { return "setup-token/" + id + "/totp" }

func adminTOTPPurpose(id string) string { return "admin/" + id + "/totp" }
