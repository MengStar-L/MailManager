package cryptox

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// LoadOrCreateKey loads a raw 32-byte key or atomically creates one with mode
// 0600. The bool result reports whether this call created the file.
func LoadOrCreateKey(path string) ([]byte, bool, error) {
	key, err := loadKey(path)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create key directory: %w", err)
	}

	key = make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, false, fmt.Errorf("generate master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		key, loadErr := loadKey(path)
		return key, false, loadErr
	}
	if err != nil {
		return nil, false, fmt.Errorf("create master key: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(key); err != nil {
		return nil, false, fmt.Errorf("write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, false, fmt.Errorf("sync master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, false, fmt.Errorf("close master key: %w", err)
	}
	written = true
	return key, true, nil
}

func loadKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("master key path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master key file permissions must be 0600 or stricter")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must contain exactly %d bytes", KeySize)
	}
	return key, nil
}
