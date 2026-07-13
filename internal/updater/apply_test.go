package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"mailmanager/internal/version"
)

type fakeServiceController struct {
	mu            sync.Mutex
	actions       []string
	contextErrors []error
	startErr      error
	stopErr       error
	stopHook      func()
}

func TestApplyRollsBackWhenInstalledVersionWriteFails(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.health.hook = func() {
		if err := os.Remove(fixture.installedVersionPath); err != nil {
			t.Errorf("remove marker: %v", err)
			return
		}
		if err := os.Mkdir(fixture.installedVersionPath, 0o700); err != nil {
			t.Errorf("replace marker with directory: %v", err)
		}
	}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected installed version write failure")
	}
	content, err := os.ReadFile(fixture.binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old-binary" {
		t.Fatalf("binary was not rolled back after marker failure: %q", content)
	}
	wantActions := []string{"stop", "start", "stop", "start"}
	if !reflect.DeepEqual(fixture.service.actions, wantActions) {
		t.Fatalf("service actions = %v, want %v", fixture.service.actions, wantActions)
	}
}

func (s *fakeServiceController) Stop(ctx context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, "stop")
	s.contextErrors = append(s.contextErrors, ctx.Err())
	if s.stopHook != nil {
		s.stopHook()
		s.stopHook = nil
	}
	return s.stopErr
}

func (s *fakeServiceController) Start(ctx context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, "start")
	s.contextErrors = append(s.contextErrors, ctx.Err())
	err := s.startErr
	s.startErr = nil
	return err
}

type fakeHealthChecker struct {
	errors        []error
	contextErrors []error
	calls         int
	hook          func()
}

func (h *fakeHealthChecker) Wait(ctx context.Context, _ string, _ time.Duration) error {
	h.calls++
	h.contextErrors = append(h.contextErrors, ctx.Err())
	if h.hook != nil {
		h.hook()
		h.hook = nil
	}
	if len(h.errors) == 0 {
		return nil
	}
	err := h.errors[0]
	h.errors = h.errors[1:]
	return err
}

type applyFixture struct {
	root                 string
	requestPath          string
	statePath            string
	installedVersionPath string
	binaryPath           string
	backupDir            string
	workDir              string
	server               *httptest.Server
	service              *fakeServiceController
	health               *fakeHealthChecker
	source               *staticReleaseSource
	options              ApplyOptions
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	workDir := filepath.Join(root, "work")
	backupDir := filepath.Join(root, "backups")
	binDir := filepath.Join(root, "bin")
	for _, directory := range []string{inbox, workDir, backupDir, binDir} {
		if err := os.Mkdir(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	oldBinary := []byte("old-binary")
	newBinary := []byte("new-binary")
	binaryPath := filepath.Join(binDir, "mailmanager")
	if err := os.WriteFile(binaryPath, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/binary":
			_, _ = w.Write(newBinary)
		case "/checksums":
			fmt.Fprintf(w, "%s  mailmanager-linux-amd64\n", hex.EncodeToString(digest[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	service := &fakeServiceController{}
	health := &fakeHealthChecker{}
	source := &staticReleaseSource{release: testRelease("1.1.0")}
	source.release.Assets["mailmanager-linux-amd64"] = Asset{Name: "mailmanager-linux-amd64", URL: server.URL + "/binary", Size: int64(len(newBinary))}
	source.release.Assets["checksums.txt"] = Asset{Name: "checksums.txt", URL: server.URL + "/checksums"}
	requestPath := filepath.Join(inbox, "request.json")
	installedVersionPath := filepath.Join(root, "installed-version")
	if err := os.WriteFile(installedVersionPath, []byte("1.0.0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(requestPath, Request{Version: "1.1.0", CurrentVersion: "1.0.0", RequestedAt: time.Now()}, 0o640); err != nil {
		t.Fatal(err)
	}
	fixture := &applyFixture{
		root: root, requestPath: requestPath, statePath: filepath.Join(root, "status.json"),
		installedVersionPath: installedVersionPath, binaryPath: binaryPath,
		backupDir: backupDir, workDir: workDir, server: server, service: service, health: health, source: source,
	}
	fixture.options = ApplyOptions{
		RequestPath: requestPath, StatePath: fixture.statePath, InstalledVersionPath: installedVersionPath,
		WorkDir: workDir, BackupDir: backupDir,
		BinaryPath: binaryPath, ServiceName: "mailmanager.service", ReadyURL: "http://ready.test/readyz",
		GOOS: "linux", GOARCH: "amd64", Source: source, HTTPClient: server.Client(), Service: service, Health: health,
		VerifyCandidate: func(path, _, _, _ string) error {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(content) != "new-binary" {
				return errors.New("unexpected candidate binary")
			}
			return nil
		},
		readInstalledVersion: readTestInstalledVersion,
	}
	t.Cleanup(server.Close)
	return fixture
}

func TestApplyReplacesBinaryAndCleansBackup(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.backupDir, "mailmanager-0.9.0.bak"), []byte("stale"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Apply(t.Context(), fixture.options); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(fixture.binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary" {
		t.Fatalf("installed binary = %q", content)
	}
	if fixture.service.actions == nil || !reflect.DeepEqual(fixture.service.actions, []string{"stop", "start"}) {
		t.Fatalf("service actions = %v", fixture.service.actions)
	}
	status, err := readDiskStatus(fixture.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateSucceeded || status.RollbackPerformed {
		t.Fatalf("unexpected status: %+v", status)
	}
	installedVersion, err := readTestInstalledVersion(fixture.installedVersionPath)
	if err != nil || installedVersion != "1.1.0" {
		t.Fatalf("installed version = %q, err=%v", installedVersion, err)
	}
	backups, err := filepath.Glob(filepath.Join(fixture.backupDir, "*.bak"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("remaining backups = %v, err=%v", backups, err)
	}
	if _, err := os.Stat(fixture.requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("request was not consumed: %v", err)
	}
}

func TestApplyChecksumFailureDoesNotStopService(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.source.release.Assets["checksums.txt"] = Asset{Name: "checksums.txt", URL: fixture.server.URL + "/binary"}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected checksum failure")
	}
	if len(fixture.service.actions) != 0 {
		t.Fatalf("service actions = %v", fixture.service.actions)
	}
	content, _ := os.ReadFile(fixture.binaryPath)
	if string(content) != "old-binary" {
		t.Fatalf("current binary changed to %q", content)
	}
	status, err := readDiskStatus(fixture.statePath)
	if err != nil || status.State != StateFailed {
		t.Fatalf("failed status = %+v, err=%v", status, err)
	}
}

func TestApplyRollsBackWhenUpdatedServiceIsNotReady(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.health.errors = []error{errors.New("not ready"), nil}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected readiness failure")
	}
	content, _ := os.ReadFile(fixture.binaryPath)
	if string(content) != "old-binary" {
		t.Fatalf("rolled-back binary = %q", content)
	}
	wantActions := []string{"stop", "start", "stop", "start"}
	if !reflect.DeepEqual(fixture.service.actions, wantActions) {
		t.Fatalf("service actions = %v, want %v", fixture.service.actions, wantActions)
	}
	status, err := readDiskStatus(fixture.statePath)
	if err != nil || status.State != StateFailed || !status.RollbackPerformed {
		t.Fatalf("rollback status = %+v, err=%v", status, err)
	}
	installedVersion, err := readTestInstalledVersion(fixture.installedVersionPath)
	if err != nil || installedVersion != "1.0.0" {
		t.Fatalf("rolled-back installed version = %q, err=%v", installedVersion, err)
	}
}

func TestApplyConsumesMalformedRequestAndIgnoresStaleLockFile(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.workDir, "update.lock"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.requestPath, []byte("not-json"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected malformed request failure")
	}
	if _, err := os.Stat(fixture.requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed request was not consumed: %v", err)
	}
}

func TestApplyRejectsMissingOrMismatchedInstalledVersion(t *testing.T) {
	for _, test := range []struct {
		name   string
		marker string
	}{
		{"missing", ""},
		{"mismatch", "0.9.0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			if test.marker == "" {
				if err := os.Remove(fixture.installedVersionPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(fixture.installedVersionPath, []byte(test.marker), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := Apply(t.Context(), fixture.options); err == nil {
				t.Fatal("expected installed version validation failure")
			}
			if fixture.source.calls != 0 {
				t.Fatalf("release source calls = %d, want 0", fixture.source.calls)
			}
			if len(fixture.service.actions) != 0 {
				t.Fatalf("service actions = %v", fixture.service.actions)
			}
		})
	}
}

func TestApplyUsesInstalledMarkerToRejectDowngrade(t *testing.T) {
	fixture := newApplyFixture(t)
	if err := os.WriteFile(fixture.installedVersionPath, []byte("2.0.0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(fixture.requestPath, Request{
		Version: "1.1.0", CurrentVersion: "2.0.0", RequestedAt: time.Now(),
	}, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected marker-based downgrade rejection")
	}
	if fixture.source.calls != 0 {
		t.Fatalf("release source calls = %d, want 0", fixture.source.calls)
	}
	content, err := os.ReadFile(fixture.binaryPath)
	if err != nil || string(content) != "old-binary" {
		t.Fatalf("binary changed during rejected downgrade: %q, err=%v", content, err)
	}
}

func TestApplyPreservesExistingVersionBackup(t *testing.T) {
	fixture := newApplyFixture(t)
	backupPath := filepath.Join(fixture.backupDir, "mailmanager-1.0.0.bak")
	if err := os.WriteFile(backupPath, []byte("preserved-backup"), 0o750); err != nil {
		t.Fatal(err)
	}
	fixture.health.errors = []error{errors.New("not ready"), nil}
	if err := Apply(t.Context(), fixture.options); err == nil {
		t.Fatal("expected rollback")
	}
	content, err := os.ReadFile(fixture.binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "preserved-backup" {
		t.Fatalf("existing backup was overwritten; restored %q", content)
	}
}

func TestApplyRollsBackWithRecoveryContextAfterCancellation(t *testing.T) {
	fixture := newApplyFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.health.hook = cancel
	fixture.health.errors = []error{context.Canceled, nil}
	if err := Apply(ctx, fixture.options); err == nil {
		t.Fatal("expected cancellation failure")
	}
	wantActions := []string{"stop", "start", "stop", "start"}
	if !reflect.DeepEqual(fixture.service.actions, wantActions) {
		t.Fatalf("service actions = %v, want %v", fixture.service.actions, wantActions)
	}
	for index, contextErr := range fixture.service.contextErrors[2:] {
		if contextErr != nil {
			t.Fatalf("recovery service context %d was canceled: %v", index, contextErr)
		}
	}
	if got := fixture.health.contextErrors[1]; got != nil {
		t.Fatalf("recovery health context was canceled: %v", got)
	}
}

func TestApplyStopFailureRestartsWithRecoveryContext(t *testing.T) {
	fixture := newApplyFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.service.stopHook = cancel
	fixture.service.stopErr = errors.New("uncertain stop failure")
	if err := Apply(ctx, fixture.options); err == nil {
		t.Fatal("expected stop failure")
	}
	if !reflect.DeepEqual(fixture.service.actions, []string{"stop", "start"}) {
		t.Fatalf("service actions = %v", fixture.service.actions)
	}
	if got := fixture.service.contextErrors[1]; got != nil {
		t.Fatalf("recovery start used canceled context: %v", got)
	}
}

func TestVerifyCandidateBuildInfoIsStatic(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	name := "mailmanager-candidate"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(t.TempDir(), name)
	targetVersion := "1.2.3"
	marker := version.MarkerFor(targetVersion, runtime.GOOS, runtime.GOARCH)
	command := exec.Command("go", "build", "-ldflags", "-X mailmanager/internal/version.BuildMarker="+marker, "-o", candidate, "./cmd/mailmanager")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, output)
	}
	if err := VerifyCandidateBuildInfo(candidate, targetVersion, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("verify legitimate candidate: %v", err)
	}
	if err := VerifyCandidateBuildInfo(candidate, "1.2.4", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("expected wrong release version to be rejected")
	}
	wrongArch := "arm64"
	if runtime.GOARCH == wrongArch {
		wrongArch = "amd64"
	}
	if err := VerifyCandidateBuildInfo(candidate, targetVersion, runtime.GOOS, wrongArch); err == nil {
		t.Fatal("expected wrong architecture to be rejected")
	}
	notGo := filepath.Join(t.TempDir(), "not-go")
	if err := os.WriteFile(notGo, []byte("not a Go executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCandidateBuildInfo(notGo, targetVersion, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("expected non-Go candidate to be rejected")
	}
}

func readTestInstalledVersion(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return NormalizeVersion(string(bytes.TrimSpace(content)))
}
