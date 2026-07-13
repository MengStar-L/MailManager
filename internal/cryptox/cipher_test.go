package cryptox

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestCipherRoundTripAndPurposeBinding(t *testing.T) {
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	first, err := cipher.Seal([]byte("secret"), "account:one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Seal([]byte("secret"), "account:one")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("random nonces must produce distinct envelopes")
	}
	plain, err := cipher.Open(first, "account:one")
	if err != nil || string(plain) != "secret" {
		t.Fatalf("round trip: %q, %v", plain, err)
	}
	if _, err := cipher.Open(first, "account:two"); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("wrong purpose error = %v", err)
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "master.key")
	first, created, err := LoadOrCreateKey(path)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	second, created, err := LoadOrCreateKey(path)
	if err != nil || created {
		t.Fatalf("load: created=%v err=%v", created, err)
	}
	if !bytes.Equal(first, second) || len(first) != KeySize {
		t.Fatal("loaded key differs from created key")
	}
}
