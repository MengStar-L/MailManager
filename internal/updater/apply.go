package updater

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mailmanager/internal/version"
)

const (
	maxBinaryBytes          = int64(250 << 20)
	maxChecksumBytes        = int64(1 << 20)
	maxRequestBytes         = int64(16 << 10)
	maxInstalledVersionSize = int64(128)
	recoveryTimeout         = 90 * time.Second
)

type ServiceController interface {
	Stop(context.Context, string) error
	Start(context.Context, string) error
}

type HealthChecker interface {
	Wait(context.Context, string, time.Duration) error
}

type ApplyOptions struct {
	RequestPath          string
	StatePath            string
	InstalledVersionPath string
	WorkDir              string
	BackupDir            string
	BinaryPath           string
	ServiceName          string
	ReadyURL             string
	GOOS                 string
	GOARCH               string
	Source               ReleaseSource
	HTTPClient           *http.Client
	Service              ServiceController
	Health               HealthChecker
	VerifyCandidate      func(string, string, string, string) error
	Now                  func() time.Time

	readInstalledVersion func(string) (string, error)
}

func Apply(ctx context.Context, options ApplyOptions) error {
	options = applyDefaults(options)
	if options.GOOS != "linux" {
		return errors.New("update-apply is only supported on Linux")
	}
	if _, supported := BinaryAssetName(options.GOARCH); !supported {
		return fmt.Errorf("unsupported Linux architecture %q", options.GOARCH)
	}
	if err := os.MkdirAll(options.WorkDir, 0o750); err != nil {
		return fmt.Errorf("create updater work directory: %w", err)
	}
	if err := os.MkdirAll(options.BackupDir, 0o750); err != nil {
		return fmt.Errorf("create updater backup directory: %w", err)
	}
	lock, err := acquireProcessLock(filepath.Join(options.WorkDir, "update.lock"))
	if err != nil {
		return err
	}
	defer lock()

	defer os.Remove(options.RequestPath)
	request, err := readSecureRequest(options.RequestPath)
	if err != nil {
		return failApply(options, "", false, fmt.Errorf("read update request: %w", err))
	}
	target, err := NormalizeVersion(request.Version)
	if err != nil {
		return failApply(options, request.Version, false, err)
	}
	requestedCurrent, err := NormalizeVersion(request.CurrentVersion)
	if err != nil {
		return failApply(options, target, false, fmt.Errorf("invalid current version: %w", err))
	}
	current, err := options.readInstalledVersion(options.InstalledVersionPath)
	if err != nil {
		return failApply(options, target, false, fmt.Errorf("read installed version marker: %w", err))
	}
	if current != requestedCurrent {
		return failApply(options, target, false, fmt.Errorf("installed version %s does not match requested current version %s", current, requestedCurrent))
	}
	if comparison, err := CompareVersions(current, target); err != nil || comparison >= 0 {
		if err == nil {
			err = errors.New("target version must be newer than the current version")
		}
		return failApply(options, target, false, err)
	}
	if err := writeApplyState(options, DiskStatus{State: StateDownloading, TargetVersion: target, Message: "Downloading update"}); err != nil {
		return err
	}

	release, err := options.Source.Latest(ctx, true)
	if err != nil {
		return failApply(options, target, false, fmt.Errorf("recheck GitHub release: %w", err))
	}
	if release.Version != target {
		return failApply(options, target, false, errors.New("the requested version is no longer the latest stable release"))
	}
	assetName, _ := BinaryAssetName(options.GOARCH)
	binaryAsset, ok := release.Assets[assetName]
	if !ok {
		return failApply(options, target, false, fmt.Errorf("release is missing %s", assetName))
	}
	checksumAsset, ok := release.Assets["checksums.txt"]
	if !ok {
		return failApply(options, target, false, errors.New("release is missing checksums.txt"))
	}
	if binaryAsset.Size > maxBinaryBytes {
		return failApply(options, target, false, errors.New("release binary exceeds the 250 MiB limit"))
	}

	temporaryDir, err := os.MkdirTemp(options.WorkDir, "release-*")
	if err != nil {
		return failApply(options, target, false, fmt.Errorf("create download directory: %w", err))
	}
	defer os.RemoveAll(temporaryDir)
	checksumPath := filepath.Join(temporaryDir, "checksums.txt")
	binaryPath := filepath.Join(temporaryDir, assetName)
	if err := downloadFile(ctx, options.HTTPClient, checksumAsset.URL, checksumPath, maxChecksumBytes); err != nil {
		return failApply(options, target, false, fmt.Errorf("download checksums: %w", err))
	}
	if err := downloadFile(ctx, options.HTTPClient, binaryAsset.URL, binaryPath, maxBinaryBytes); err != nil {
		return failApply(options, target, false, fmt.Errorf("download update binary: %w", err))
	}
	wantDigest, err := checksumFor(checksumPath, assetName)
	if err != nil {
		return failApply(options, target, false, err)
	}
	if err := verifyChecksum(binaryPath, wantDigest); err != nil {
		return failApply(options, target, false, err)
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return failApply(options, target, false, fmt.Errorf("mark update binary executable: %w", err))
	}
	if err := options.VerifyCandidate(binaryPath, target, options.GOOS, options.GOARCH); err != nil {
		return failApply(options, target, false, fmt.Errorf("verify update binary build information: %w", err))
	}

	backupPath := filepath.Join(options.BackupDir, "mailmanager-"+current+".bak")
	pruneBackups(options.BackupDir, backupPath)
	if err := copyFileAtomicIfAbsent(options.BinaryPath, backupPath, 0o750); err != nil {
		return failApply(options, target, false, fmt.Errorf("back up current binary: %w", err))
	}
	if err := writeApplyState(options, DiskStatus{State: StateInstalling, TargetVersion: target, Message: "Installing update"}); err != nil {
		return err
	}
	if err := options.Service.Stop(ctx, options.ServiceName); err != nil {
		recoveryCtx, cancel := recoveryContext(ctx)
		startErr := options.Service.Start(recoveryCtx, options.ServiceName)
		cancel()
		return failApply(options, target, false, errors.Join(fmt.Errorf("stop MailManager service: %w", err), wrapRecoveryStartError(startErr)))
	}
	if err := copyFileAtomic(binaryPath, options.BinaryPath, 0o755); err != nil {
		recoveryCtx, cancel := recoveryContext(ctx)
		startErr := options.Service.Start(recoveryCtx, options.ServiceName)
		cancel()
		return failApply(options, target, false, errors.Join(fmt.Errorf("replace MailManager binary: %w", err), wrapRecoveryStartError(startErr)))
	}
	if err := writeApplyState(options, DiskStatus{State: StateRestarting, TargetVersion: target, Message: "Restarting MailManager"}); err != nil {
		return rollbackApply(ctx, options, target, current, backupPath, err)
	}
	if err := options.Service.Start(ctx, options.ServiceName); err != nil {
		return rollbackApply(ctx, options, target, current, backupPath, fmt.Errorf("start updated MailManager service: %w", err))
	}
	if err := options.Health.Wait(ctx, options.ReadyURL, 60*time.Second); err != nil {
		return rollbackApply(ctx, options, target, current, backupPath, fmt.Errorf("updated service did not become ready: %w", err))
	}
	if err := writeInstalledVersion(options.InstalledVersionPath, target); err != nil {
		return rollbackApply(ctx, options, target, current, backupPath, fmt.Errorf("write installed version marker: %w", err))
	}
	if err := writeApplyState(options, DiskStatus{State: StateSucceeded, TargetVersion: target, Message: "Update installed successfully"}); err != nil {
		return rollbackApply(ctx, options, target, current, backupPath, fmt.Errorf("write successful update status: %w", err))
	}
	_ = os.Remove(backupPath)
	return nil
}

func applyDefaults(options ApplyOptions) ApplyOptions {
	if options.RequestPath == "" {
		options.RequestPath = filepath.Join(DefaultInboxDir, "request.json")
	}
	if options.StatePath == "" {
		options.StatePath = DefaultStatePath
	}
	if options.InstalledVersionPath == "" {
		options.InstalledVersionPath = DefaultInstalledVersionPath
	}
	if options.WorkDir == "" {
		options.WorkDir = DefaultWorkDir
	}
	if options.BackupDir == "" {
		options.BackupDir = DefaultBackupDir
	}
	if options.BinaryPath == "" {
		options.BinaryPath = DefaultBinary
	}
	if options.ServiceName == "" {
		options.ServiceName = DefaultService
	}
	if options.ReadyURL == "" {
		options.ReadyURL = DefaultReadyURL
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Source == nil {
		options.Source = NewGitHubClient(GitHubClientOptions{})
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	if options.Service == nil {
		options.Service = SystemdController{}
	}
	if options.Health == nil {
		options.Health = HTTPHealthChecker{Client: &http.Client{Timeout: 5 * time.Second}}
	}
	if options.VerifyCandidate == nil {
		options.VerifyCandidate = VerifyCandidateBuildInfo
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.readInstalledVersion == nil {
		options.readInstalledVersion = readInstalledVersion
	}
	return options
}

func readSecureRequest(path string) (Request, error) {
	content, err := readRegularFileNoFollow(path, maxRequestBytes)
	if err != nil {
		return Request{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	if request.Version == "" || request.CurrentVersion == "" {
		return Request{}, errors.New("update request is incomplete")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("update request contains trailing data")
	}
	return request, nil
}

func failApply(options ApplyOptions, target string, rollback bool, cause error) error {
	stateErr := writeApplyState(options, DiskStatus{
		State: StateFailed, TargetVersion: target, Message: cleanMessage(cause), RollbackPerformed: rollback,
	})
	if stateErr != nil {
		return errors.Join(cause, fmt.Errorf("write failed update status: %w", stateErr))
	}
	return cause
}

func rollbackApply(ctx context.Context, options ApplyOptions, target, current, backupPath string, cause error) error {
	recoveryCtx, cancel := recoveryContext(ctx)
	defer cancel()

	stopErr := options.Service.Stop(recoveryCtx, options.ServiceName)
	restoreErr := copyFileAtomic(backupPath, options.BinaryPath, 0o755)
	var markerErr error
	if restoreErr == nil {
		markerErr = writeInstalledVersion(options.InstalledVersionPath, current)
	}
	startErr := options.Service.Start(recoveryCtx, options.ServiceName)
	var healthErr error
	if startErr == nil {
		healthErr = options.Health.Wait(recoveryCtx, options.ReadyURL, 60*time.Second)
	}
	rollbackComplete := restoreErr == nil && markerErr == nil && startErr == nil && healthErr == nil
	return failApply(options, target, rollbackComplete, errors.Join(
		cause,
		wrapError("stop updated service during rollback", stopErr),
		wrapError("restore previous binary", restoreErr),
		wrapError("restore installed version marker", markerErr),
		wrapError("restart rolled-back service", startErr),
		wrapError("rolled-back service did not become ready", healthErr),
	))
}

func recoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recoveryTimeout)
}

func wrapRecoveryStartError(err error) error {
	return wrapError("restart current MailManager service", err)
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func writeApplyState(options ApplyOptions, status DiskStatus) error {
	status.UpdatedAt = options.Now().UTC()
	return writeJSONAtomic(options.StatePath, status, 0o640)
}

func readInstalledVersion(path string) (string, error) {
	content, err := readRootOwnedRegularFileNoFollow(path, maxInstalledVersionSize)
	if err != nil {
		return "", err
	}
	version, err := NormalizeVersion(strings.TrimSpace(string(content)))
	if err != nil {
		return "", fmt.Errorf("invalid installed version marker: %w", err)
	}
	return version, nil
}

func writeInstalledVersion(path, version string) error {
	normalized, err := NormalizeVersion(version)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(normalized+"\n"), 0o640)
}

func downloadFile(ctx context.Context, client *http.Client, rawURL, destination string, limit int64) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("release asset URL must be HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "MailManager-updater")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" {
		return errors.New("release asset redirect must remain on HTTPS")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("asset server returned status %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return fmt.Errorf("asset exceeds the %d byte limit", limit)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if written > limit {
		return fmt.Errorf("asset exceeds the %d byte limit", limit)
	}
	return errors.Join(syncErr, closeErr)
}

func pruneBackups(directory, keep string) {
	entries, err := filepath.Glob(filepath.Join(directory, "mailmanager-*.bak"))
	if err != nil {
		return
	}
	for _, path := range entries {
		if path != keep {
			_ = os.Remove(path)
		}
	}
}

func copyFileAtomicIfAbsent(source, destination string, mode os.FileMode) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("existing update backup is not a regular file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".mailmanager-backup-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(destination)
			if statErr == nil && info.Mode().IsRegular() {
				return nil
			}
		}
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func checksumFor(path, filename string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		listedName := strings.TrimPrefix(fields[1], "*")
		if listedName != filename || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("checksums.txt contains an invalid SHA-256 digest")
		}
		return strings.ToLower(fields[0]), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksums.txt does not contain %s", filename)
}

func verifyChecksum(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != expected {
		return fmt.Errorf("SHA-256 mismatch: got %s", actual)
	}
	return nil
}

func copyFileAtomic(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".mailmanager-binary-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

type SystemdController struct{}

func (SystemdController) Stop(ctx context.Context, service string) error {
	return exec.CommandContext(ctx, "systemctl", "stop", service).Run()
}

func (SystemdController) Start(ctx context.Context, service string) error {
	return exec.CommandContext(ctx, "systemctl", "start", service).Run()
}

type HTTPHealthChecker struct {
	Client *http.Client
}

func (h HTTPHealthChecker) Wait(ctx context.Context, readyURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err != nil {
			return err
		}
		response, err := h.Client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("readiness timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func VerifyCandidateBuildInfo(path, target, goos, goarch string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Go build information: %w", err)
	}
	if info.Path != "mailmanager/cmd/mailmanager" || info.Main.Path != "mailmanager" {
		return fmt.Errorf("unexpected Go main package %q in module %q", info.Path, info.Main.Path)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != goos || settings["GOARCH"] != goarch {
		return fmt.Errorf("candidate targets %s/%s, expected %s/%s", settings["GOOS"], settings["GOARCH"], goos, goarch)
	}
	marker := version.MarkerFor(target, goos, goarch)
	found, err := fileContainsMarker(path, []byte(marker))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("candidate is missing release marker %q", marker)
	}
	return nil
}

func fileContainsMarker(path string, marker []byte) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 64<<10)
	tail := make([]byte, 0, len(marker)-1)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			window := make([]byte, 0, len(tail)+n)
			window = append(window, tail...)
			window = append(window, buffer[:n]...)
			if bytes.Contains(window, marker) {
				return true, nil
			}
			keep := len(marker) - 1
			if keep > len(window) {
				keep = len(window)
			}
			tail = append(tail[:0], window[len(window)-keep:]...)
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}
