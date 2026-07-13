//go:build unix

package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNoFollowRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := readRegularFileNoFollow(path, maxRequestBytes)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected FIFO to be rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("opening FIFO blocked; O_NONBLOCK is required")
	}
}

func TestInstalledVersionRequiresRootOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed-version")
	if err := os.WriteFile(path, []byte("1.0.0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := readRootOwnedRegularFileNoFollow(path, maxInstalledVersionSize)
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("root-owned marker was rejected: %v", err)
		}
	} else if err == nil {
		t.Fatal("non-root-owned marker was accepted")
	}
}
