package updater

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	RepositoryOwner = "MengStar-L"
	RepositoryName  = "MailManager"

	DefaultInboxDir             = "/var/lib/mailmanager-updater/inbox"
	DefaultStatePath            = "/var/lib/mailmanager-updater/status.json"
	DefaultInstalledVersionPath = "/var/lib/mailmanager-updater/installed-version"
	DefaultWorkDir              = "/var/lib/mailmanager-updater/work"
	DefaultBackupDir            = "/var/lib/mailmanager-updater/backups"
	DefaultBinary               = "/opt/mailmanager/bin/mailmanager"
	DefaultService              = "mailmanager.service"
	DefaultReadyURL             = "http://127.0.0.1:8080/readyz"
)

type State string

const (
	StateIdle        State = "idle"
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateInstalling  State = "installing"
	StateRestarting  State = "restarting"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
)

func (s State) Active() bool {
	return s == StateQueued || s == StateDownloading || s == StateInstalling || s == StateRestarting
}

type Asset struct {
	Name string
	URL  string
	Size int64
}

type Release struct {
	Version      string
	TagName      string
	Name         string
	PublishedAt  time.Time
	ReleaseNotes string
	HTMLURL      string
	CheckedAt    time.Time
	Assets       map[string]Asset
}

type ReleaseSummary struct {
	Version      string    `json:"version"`
	TagName      string    `json:"tag_name"`
	Name         string    `json:"name"`
	PublishedAt  time.Time `json:"published_at"`
	ReleaseNotes string    `json:"release_notes"`
	HTMLURL      string    `json:"html_url"`
}

func (r Release) Summary() *ReleaseSummary {
	return &ReleaseSummary{
		Version: r.Version, TagName: r.TagName, Name: r.Name, PublishedAt: r.PublishedAt,
		ReleaseNotes: r.ReleaseNotes, HTMLURL: r.HTMLURL,
	}
}

type Status struct {
	CurrentVersion    string          `json:"current_version"`
	CurrentCommit     string          `json:"current_commit"`
	CurrentBuildTime  string          `json:"current_build_time"`
	Enabled           bool            `json:"enabled"`
	InstallSupported  bool            `json:"install_supported"`
	State             State           `json:"state"`
	Available         bool            `json:"available"`
	CheckedAt         *time.Time      `json:"checked_at,omitempty"`
	Latest            *ReleaseSummary `json:"latest,omitempty"`
	TargetVersion     string          `json:"target_version,omitempty"`
	Message           string          `json:"message,omitempty"`
	RollbackPerformed bool            `json:"rollback_performed"`
}

type Request struct {
	Version        string    `json:"version"`
	CurrentVersion string    `json:"current_version"`
	RequestedAt    time.Time `json:"requested_at"`
}

type DiskStatus struct {
	State             State     `json:"state"`
	TargetVersion     string    `json:"target_version,omitempty"`
	Message           string    `json:"message,omitempty"`
	RollbackPerformed bool      `json:"rollback_performed"`
	UpdatedAt         time.Time `json:"updated_at"`
}

var (
	ErrConflict    = errors.New("an update is already in progress")
	ErrUnsupported = errors.New("automatic installation is not supported")
	ErrVersion     = errors.New("invalid update version")
	stableVersion  = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z.-]+)?$`)
)

func NormalizeVersion(value string) (string, error) {
	matches := stableVersion.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return "", ErrVersion
	}
	return matches[1] + "." + matches[2] + "." + matches[3], nil
}

func CompareVersions(left, right string) (int, error) {
	l, err := versionParts(left)
	if err != nil {
		return 0, err
	}
	r, err := versionParts(right)
	if err != nil {
		return 0, err
	}
	for i := range l {
		if l[i] < r[i] {
			return -1, nil
		}
		if l[i] > r[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func versionParts(value string) ([3]uint64, error) {
	var result [3]uint64
	normalized, err := NormalizeVersion(value)
	if err != nil {
		return result, err
	}
	for i, part := range strings.Split(normalized, ".") {
		result[i], err = strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("parse version %q: %w", value, err)
		}
	}
	return result, nil
}

func BinaryAssetName(goarch string) (string, bool) {
	switch goarch {
	case "amd64", "arm64":
		return "mailmanager-linux-" + goarch, true
	default:
		return "", false
	}
}
