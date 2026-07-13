package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidPasswordHash = errors.New("invalid Argon2id password hash")

type PasswordParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
}

type PasswordHasher struct {
	params PasswordParams
}

func NewPasswordHasher(params PasswordParams) (*PasswordHasher, error) {
	if err := validatePasswordParams(params); err != nil {
		return nil, err
	}
	return &PasswordHasher{params: params}, nil
}

func HashPassword(password string) (string, error) {
	hasher, err := NewPasswordHasher(DefaultPasswordParams())
	if err != nil {
		return "", err
	}
	return hasher.Hash(password)
}

func VerifyPassword(encoded, password string) (bool, error) {
	return verifyPassword(encoded, password)
}

func (h *PasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	digest := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}

func (h *PasswordHasher) Verify(encoded, password string) (bool, error) {
	return verifyPassword(encoded, password)
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidPasswordHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidPasswordHash
	}
	params := PasswordParams{}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.MemoryKiB, &params.Iterations, &params.Parallelism); err != nil {
		return false, ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(digest))
	if err := validatePasswordParams(params); err != nil {
		return false, ErrInvalidPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
	return subtle.ConstantTimeCompare(actual, digest) == 1, nil
}

func validatePasswordParams(params PasswordParams) error {
	if params.MemoryKiB < 8 || params.MemoryKiB > 1024*1024 || params.Iterations < 1 || params.Iterations > 20 ||
		params.Parallelism < 1 || params.Parallelism > 16 || params.SaltLength < 8 || params.SaltLength > 64 ||
		params.KeyLength < 16 || params.KeyLength > 64 {
		return errors.New("invalid Argon2id parameters")
	}
	return nil
}
