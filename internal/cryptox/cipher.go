package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	KeySize         = 32
	envelopeVersion = byte(1)
)

var ErrInvalidCiphertext = errors.New("invalid encrypted secret")

// Cipher encrypts individual secret values with AES-256-GCM. Callers must use
// a stable, record-specific purpose as additional authenticated data.
type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plaintext []byte, purpose string) ([]byte, error) {
	if purpose == "" {
		return nil, errors.New("encryption purpose is required")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 1, 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	out[0] = envelopeVersion
	out = append(out, nonce...)
	out = c.aead.Seal(out, nonce, plaintext, []byte(purpose))
	return out, nil
}

func (c *Cipher) Open(envelope []byte, purpose string) ([]byte, error) {
	minimum := 1 + c.aead.NonceSize() + c.aead.Overhead()
	if purpose == "" || len(envelope) < minimum || envelope[0] != envelopeVersion {
		return nil, ErrInvalidCiphertext
	}
	nonce := envelope[1 : 1+c.aead.NonceSize()]
	ciphertext := envelope[1+c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}

func (c *Cipher) SealString(plaintext, purpose string) (string, error) {
	envelope, err := c.Seal([]byte(plaintext), purpose)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func (c *Cipher) OpenString(encoded, purpose string) (string, error) {
	envelope, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	plaintext, err := c.Open(envelope, purpose)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
