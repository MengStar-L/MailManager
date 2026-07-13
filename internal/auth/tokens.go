package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const opaqueTokenBytes = 32

var recoveryAlphabet = []byte("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")

func GenerateOpaqueToken() (string, error) {
	raw := make([]byte, opaqueTokenBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashOpaqueToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func GenerateRecoveryCodes(count int) ([]string, error) {
	if count <= 0 || count > 100 {
		return nil, fmt.Errorf("invalid recovery code count %d", count)
	}
	codes := make([]string, count)
	for i := range codes {
		random := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		encoded := make([]byte, len(random))
		for j, value := range random {
			encoded[j] = recoveryAlphabet[int(value)&31]
		}
		codes[i] = string(encoded[0:4]) + "-" + string(encoded[4:8]) + "-" + string(encoded[8:12]) + "-" + string(encoded[12:16])
	}
	return codes, nil
}

func HashRecoveryCode(code string) []byte {
	normalized := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	digest := sha256.Sum256([]byte(normalized))
	return digest[:]
}
