package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Service interface {
	Status(context.Context) (Status, error)
	Check(context.Context) (Status, error)
	Install(context.Context, string) (Status, error)
}

type ManagerOptions struct {
	Enabled              bool
	CurrentVersion       string
	CurrentCommit        string
	BuildTime            string
	InboxDir             string
	StatePath            string
	InstalledVersionPath string
	GOOS                 string
	GOARCH               string
	Source               ReleaseSource
	Now                  func() time.Time

	readInstalledVersion func(string) (string, error)
}

type Manager struct {
	enabled              bool
	currentVersion       string
	currentCommit        string
	buildTime            string
	inboxDir             string
	statePath            string
	installedVersionPath string
	goos                 string
	goarch               string
	source               ReleaseSource
	now                  func() time.Time
	readInstalledVersion func(string) (string, error)

	mu        sync.Mutex
	latest    Release
	hasLatest bool
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Source == nil {
		return nil, errors.New("update release source is required")
	}
	if options.InboxDir == "" {
		options.InboxDir = DefaultInboxDir
	}
	if options.StatePath == "" {
		options.StatePath = DefaultStatePath
	}
	if options.InstalledVersionPath == "" {
		options.InstalledVersionPath = DefaultInstalledVersionPath
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.readInstalledVersion == nil {
		options.readInstalledVersion = readInstalledVersion
	}
	return &Manager{
		enabled: options.Enabled, currentVersion: options.CurrentVersion, currentCommit: options.CurrentCommit,
		buildTime: options.BuildTime, inboxDir: options.InboxDir, statePath: options.StatePath,
		installedVersionPath: options.InstalledVersionPath,
		goos:                 options.GOOS, goarch: options.GOARCH, source: options.Source, now: options.Now,
		readInstalledVersion: options.readInstalledVersion,
	}, nil
}

func (m *Manager) Run(ctx context.Context) {
	_, _ = m.check(ctx, false)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.check(ctx, false)
		}
	}
}

func (m *Manager) Status(context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) Check(ctx context.Context) (Status, error) {
	return m.check(ctx, true)
}

func (m *Manager) check(ctx context.Context, force bool) (Status, error) {
	release, err := m.source.Latest(ctx, force)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest, m.hasLatest = release, true
	return m.statusLocked()
}

func (m *Manager) Install(ctx context.Context, requested string) (Status, error) {
	requested, err := NormalizeVersion(requested)
	if err != nil {
		return Status{}, err
	}
	release, err := m.source.Latest(ctx, true)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest, m.hasLatest = release, true
	status, err := m.statusLocked()
	if err != nil {
		return Status{}, err
	}
	if !status.InstallSupported {
		return status, ErrUnsupported
	}
	if requested != release.Version || !status.Available {
		return status, fmt.Errorf("%w: requested %s is not the latest available release", ErrVersion, requested)
	}
	requestPath := filepath.Join(m.inboxDir, "request.json")
	if status.State.Active() || fileExists(requestPath) {
		return status, ErrConflict
	}
	request := Request{Version: requested, CurrentVersion: m.currentVersion, RequestedAt: m.now().UTC()}
	if err := writeJSONAtomic(requestPath, request, 0o640); err != nil {
		return status, fmt.Errorf("queue update request: %w", err)
	}
	status.State = StateQueued
	status.TargetVersion = requested
	status.Message = "Update queued"
	return status, nil
}

func (m *Manager) statusLocked() (Status, error) {
	status := Status{
		CurrentVersion: m.currentVersion, CurrentCommit: m.currentCommit, CurrentBuildTime: m.buildTime,
		Enabled: m.enabled, State: StateIdle,
	}
	if m.hasLatest {
		status.Latest = m.latest.Summary()
		checkedAt := m.latest.CheckedAt
		status.CheckedAt = &checkedAt
		if comparison, err := CompareVersions(m.currentVersion, m.latest.Version); err == nil {
			status.Available = comparison < 0
		}
	}
	status.InstallSupported, status.Message = m.installCapabilityLocked()
	if disk, err := readDiskStatus(m.statePath); err == nil {
		status.State = disk.State
		status.TargetVersion = disk.TargetVersion
		status.Message = disk.Message
		status.RollbackPerformed = disk.RollbackPerformed
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("read updater status: %w", err)
	}
	requestPath := filepath.Join(m.inboxDir, "request.json")
	requestExists := fileExists(requestPath)
	if status.State.Active() && !requestExists {
		status.State = StateFailed
		status.Message = "The privileged update helper exited or was interrupted"
	}
	if !status.State.Active() && requestExists {
		status.State = StateQueued
		if request, err := readRequest(requestPath); err == nil {
			status.TargetVersion = request.Version
		}
	}
	return status, nil
}

func (m *Manager) installCapabilityLocked() (bool, string) {
	if !m.enabled {
		return false, "Automatic updates are disabled"
	}
	if m.goos != "linux" {
		return false, "Automatic installation requires Linux"
	}
	assetName, supported := BinaryAssetName(m.goarch)
	if !supported {
		return false, "This CPU architecture is not supported"
	}
	if _, err := NormalizeVersion(m.currentVersion); err != nil {
		return false, "Development builds cannot be updated automatically"
	}
	info, err := os.Stat(m.inboxDir)
	if err != nil || !info.IsDir() {
		return false, "The privileged update helper is not installed"
	}
	installedVersion, err := m.readInstalledVersion(m.installedVersionPath)
	if err != nil {
		return false, "The privileged update helper/bootstrap is not installed"
	}
	currentVersion, _ := NormalizeVersion(m.currentVersion)
	if installedVersion != currentVersion {
		return false, "The installed version marker does not match the current version"
	}
	if m.hasLatest {
		if _, ok := m.latest.Assets[assetName]; !ok {
			return false, "The latest release does not include a binary for this server"
		}
		if _, ok := m.latest.Assets["checksums.txt"]; !ok {
			return false, "The latest release does not include checksums.txt"
		}
	}
	return true, ""
}

func readDiskStatus(path string) (DiskStatus, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return DiskStatus{}, err
	}
	var status DiskStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return DiskStatus{}, err
	}
	if status.State == "" {
		status.State = StateIdle
	}
	return status, nil
}

func readRequest(path string) (Request, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err := json.Unmarshal(content, &request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(content, '\n'), mode)
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".mailmanager-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cleanMessage(err error) string {
	return strings.TrimSpace(err.Error())
}
