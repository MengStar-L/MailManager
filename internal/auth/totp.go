package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 interoperability requires HMAC-SHA1 by default.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	totpPeriod = int64(30)
	totpDigits = 6
)

func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(at.UTC().Unix() / totpPeriod)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 |
		uint32(digest[offset+1])<<16 |
		uint32(digest[offset+2])<<8 |
		uint32(digest[offset+3])
	value %= 1_000_000
	return fmt.Sprintf("%0*d", totpDigits, value), nil
}

func VerifyTOTP(secret, code string, at time.Time) bool {
	if len(code) != totpDigits {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	for offset := -1; offset <= 1; offset++ {
		expected, err := TOTPCode(secret, at.Add(time.Duration(offset*int(totpPeriod))*time.Second))
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func TOTPProvisioningURI(issuer, account, secret string) (string, error) {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(account) == "" {
		return "", errors.New("TOTP issuer and account are required")
	}
	if _, err := decodeTOTPSecret(secret); err != nil {
		return "", err
	}
	u := &url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + issuer + ":" + account}
	query := url.Values{}
	query.Set("secret", strings.ToUpper(secret))
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", strconv.Itoa(totpDigits))
	query.Set("period", strconv.FormatInt(totpPeriod, 10))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(decoded) < 16 {
		return nil, errors.New("invalid TOTP secret")
	}
	return decoded, nil
}
