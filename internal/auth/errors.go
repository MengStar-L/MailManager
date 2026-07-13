package auth

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrAlreadyConfigured   = errors.New("mailmanager is already configured")
	ErrSetupTokenInvalid   = errors.New("setup token is invalid or expired")
	ErrEnrollmentRequired  = errors.New("TOTP enrollment is required")
	ErrWeakPassword        = errors.New("password does not meet requirements")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidTOTP         = errors.New("invalid TOTP code")
	ErrChallengeInvalid    = errors.New("login challenge is invalid or expired")
	ErrSessionInvalid      = errors.New("session is invalid or expired")
	ErrCSRFInvalid         = errors.New("CSRF token is invalid")
	ErrRateLimited         = errors.New("too many login attempts")
	ErrRecoveryCodeInvalid = errors.New("recovery code is invalid or already used")
)

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s; retry in %s", ErrRateLimited, e.RetryAfter.Round(time.Second))
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }
