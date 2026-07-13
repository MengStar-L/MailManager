package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type staticReleaseSource struct {
	release Release
	err     error
	calls   int
}

func (s *staticReleaseSource) Latest(context.Context, bool) (Release, error) {
	s.calls++
	return s.release, s.err
}

func testRelease(version string) Release {
	return Release{
		Version: version, TagName: "v" + version, CheckedAt: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
		Assets: map[string]Asset{
			"mailmanager-linux-amd64": {Name: "mailmanager-linux-amd64", URL: "https://example.test/binary"},
			"checksums.txt":           {Name: "checksums.txt", URL: "https://example.test/checksums"},
		},
	}
}

func TestManagerChecksAndQueuesLatestRelease(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.Mkdir(inbox, 0o770); err != nil {
		t.Fatal(err)
	}
	source := &staticReleaseSource{release: testRelease("1.2.0")}
	installedVersionPath := filepath.Join(root, "installed-version")
	if err := os.WriteFile(installedVersionPath, []byte("1.1.0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		Enabled: true, CurrentVersion: "1.1.0", CurrentCommit: "abc", BuildTime: "now",
		InboxDir: inbox, StatePath: filepath.Join(root, "status.json"), GOOS: "linux", GOARCH: "amd64",
		InstalledVersionPath: installedVersionPath, readInstalledVersion: readTestInstalledVersion,
		Source: source, Now: func() time.Time { return time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || !status.InstallSupported || status.Latest == nil || status.Latest.Version != "1.2.0" {
		t.Fatalf("unexpected checked status: %+v", status)
	}
	queued, err := manager.Install(t.Context(), "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != StateQueued || queued.TargetVersion != "1.2.0" {
		t.Fatalf("unexpected queued status: %+v", queued)
	}
	request, err := readRequest(filepath.Join(inbox, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if request.Version != "1.2.0" || request.CurrentVersion != "1.1.0" {
		t.Fatalf("unexpected request: %+v", request)
	}
	if _, err := manager.Install(t.Context(), "1.2.0"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second install error = %v", err)
	}
}

func TestManagerDisablesInstallWhenBootstrapMarkerIsMissingOrMismatched(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.Mkdir(inbox, 0o770); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "installed-version")
	for _, test := range []struct {
		name, value, message string
	}{
		{"missing", "", "bootstrap"},
		{"mismatch", "0.9.0\n", "does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(marker)
			if test.value != "" {
				if err := os.WriteFile(marker, []byte(test.value), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := NewManager(ManagerOptions{
				Enabled: true, CurrentVersion: "1.0.0", InboxDir: inbox, StatePath: filepath.Join(root, test.name+"-status.json"),
				InstalledVersionPath: marker, GOOS: "linux", GOARCH: "amd64",
				Source: &staticReleaseSource{release: testRelease("1.2.0")}, readInstalledVersion: readTestInstalledVersion,
			})
			if err != nil {
				t.Fatal(err)
			}
			status, err := manager.Check(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if status.InstallSupported || !strings.Contains(status.Message, test.message) {
				t.Fatalf("unexpected capability status: %+v", status)
			}
		})
	}
}

func TestManagerMarksOrphanedActiveStateFailedAndAllowsRetry(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.Mkdir(inbox, 0o770); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "installed-version")
	if err := os.WriteFile(marker, []byte("1.0.0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "status.json")
	if err := writeJSONAtomic(statePath, DiskStatus{
		State: StateDownloading, TargetVersion: "1.1.0", Message: "Downloading update",
	}, 0o640); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		Enabled: true, CurrentVersion: "1.0.0", InboxDir: inbox, StatePath: statePath,
		InstalledVersionPath: marker, GOOS: "linux", GOARCH: "amd64",
		Source: &staticReleaseSource{release: testRelease("1.1.0")}, readInstalledVersion: readTestInstalledVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateFailed || status.State.Active() || status.TargetVersion != "1.1.0" || !strings.Contains(status.Message, "interrupted") {
		t.Fatalf("unexpected orphaned state: %+v", status)
	}
	queued, err := manager.Install(t.Context(), "1.1.0")
	if err != nil {
		t.Fatalf("retry install: %v", err)
	}
	if queued.State != StateQueued || queued.TargetVersion != "1.1.0" {
		t.Fatalf("unexpected retry state: %+v", queued)
	}
}

func TestManagerRejectsUnsupportedInstallations(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inbox"), 0o770); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, version, goos, goarch string
		enabled                     bool
	}{
		{"disabled", "1.0.0", "linux", "amd64", false},
		{"development", "dev", "linux", "amd64", true},
		{"non-linux", "1.0.0", "windows", "amd64", true},
		{"architecture", "1.0.0", "linux", "386", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(ManagerOptions{
				Enabled: test.enabled, CurrentVersion: test.version, InboxDir: filepath.Join(root, "inbox"),
				StatePath: filepath.Join(root, test.name+".json"), GOOS: test.goos, GOARCH: test.goarch,
				Source: &staticReleaseSource{release: testRelease("1.2.0")},
			})
			if err != nil {
				t.Fatal(err)
			}
			status, err := manager.Check(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if status.InstallSupported {
				t.Fatalf("unexpected supported status: %+v", status)
			}
		})
	}
}
