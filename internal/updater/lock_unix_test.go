//go:build unix

package updater

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestProcessLockUsesKernelLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	releaseFirst, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireProcessLock(path); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent lock error = %v", err)
	}
	releaseFirst()
	releaseSecond, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("persistent lock file blocked a later process: %v", err)
	}
	releaseSecond()
}
