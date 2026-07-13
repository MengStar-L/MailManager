package auth

import (
	"strings"
	"testing"
	"time"
)

func TestTOTPCodeAndWindow(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	at := time.Unix(1_700_000_000, 0).UTC()
	code, err := TOTPCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTOTP(secret, code, at.Add(30*time.Second)) {
		t.Fatal("previous time step must be accepted")
	}
	if VerifyTOTP(secret, code, at.Add(60*time.Second)) {
		t.Fatal("code outside the one-step window must be rejected")
	}
	uri, err := TOTPProvisioningURI("MailManager", "admin", secret)
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("provisioning URI = %q, %v", uri, err)
	}
}

func TestRateLimiterExpiresFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewRateLimiter(RateLimiterOptions{MaxFailures: 2, Window: time.Minute, Now: func() time.Time { return now }})
	limiter.RecordFailure("client")
	limiter.RecordFailure("client")
	if allowed, _ := limiter.Allow("client"); allowed {
		t.Fatal("client should be limited")
	}
	now = now.Add(time.Minute + time.Second)
	if allowed, _ := limiter.Allow("client"); !allowed {
		t.Fatal("expired failures should be pruned")
	}
}
