package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress         = "127.0.0.1:8080"
	defaultPublicURL       = "http://localhost:8080"
	defaultSessionTTL      = 30 * 24 * time.Hour
	defaultSetupTTL        = 30 * time.Minute
	defaultChallengeTTL    = 5 * time.Minute
	defaultAttachmentLimit = int64(25 << 20)
	defaultAttachmentCache = int64(5 << 30)
)

// Config contains the process-level settings shared by the HTTP server and
// background workers. Secrets themselves are never accepted through Config;
// only the path to the protected master-key file is configured here.
type Config struct {
	Address                    string
	PublicURL                  *url.URL
	DataDir                    string
	DatabasePath               string
	MasterKeyPath              string
	AttachmentCacheDir         string
	SessionTTL                 time.Duration
	SetupTTL                   time.Duration
	LoginChallengeTTL          time.Duration
	MaxAttachmentBytes         int64
	AttachmentCacheMax         int64
	SecureCookies              bool
	AutoUpdateEnabled          bool
	UpdateInboxDir             string
	UpdateStatePath            string
	UpdateInstalledVersionPath string
}

// Load reads configuration from MAILMANAGER_* environment variables.
func Load() (Config, error) {
	dataDir := envOr("MAILMANAGER_DATA_DIR", "data")
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}

	publicURL, err := parsePublicURL(envOr("MAILMANAGER_PUBLIC_URL", defaultPublicURL))
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := durationEnv("MAILMANAGER_SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	setupTTL, err := durationEnv("MAILMANAGER_SETUP_TTL", defaultSetupTTL)
	if err != nil {
		return Config{}, err
	}
	challengeTTL, err := durationEnv("MAILMANAGER_LOGIN_CHALLENGE_TTL", defaultChallengeTTL)
	if err != nil {
		return Config{}, err
	}
	maxAttachment, err := byteSizeEnv("MAILMANAGER_MAX_ATTACHMENT_BYTES", defaultAttachmentLimit)
	if err != nil {
		return Config{}, err
	}
	cacheMax, err := byteSizeEnv("MAILMANAGER_ATTACHMENT_CACHE_BYTES", defaultAttachmentCache)
	if err != nil {
		return Config{}, err
	}
	autoUpdateEnabled, err := boolEnv("MAILMANAGER_AUTO_UPDATE_ENABLED", false)
	if err != nil {
		return Config{}, err
	}

	address := envOr("MAILMANAGER_ADDR", defaultAddress)
	if _, _, err := net.SplitHostPort(address); err != nil {
		return Config{}, fmt.Errorf("MAILMANAGER_ADDR: %w", err)
	}

	return Config{
		Address:                    address,
		PublicURL:                  publicURL,
		DataDir:                    dataDir,
		DatabasePath:               pathEnv("MAILMANAGER_DATABASE_PATH", filepath.Join(dataDir, "mailmanager.db")),
		MasterKeyPath:              pathEnv("MAILMANAGER_MASTER_KEY_FILE", filepath.Join(dataDir, "master.key")),
		AttachmentCacheDir:         pathEnv("MAILMANAGER_ATTACHMENT_CACHE_DIR", filepath.Join(dataDir, "attachments")),
		SessionTTL:                 sessionTTL,
		SetupTTL:                   setupTTL,
		LoginChallengeTTL:          challengeTTL,
		MaxAttachmentBytes:         maxAttachment,
		AttachmentCacheMax:         cacheMax,
		SecureCookies:              publicURL.Scheme == "https",
		AutoUpdateEnabled:          autoUpdateEnabled,
		UpdateInboxDir:             pathEnv("MAILMANAGER_UPDATE_INBOX_DIR", "/var/lib/mailmanager-updater/inbox"),
		UpdateStatePath:            pathEnv("MAILMANAGER_UPDATE_STATE_FILE", "/var/lib/mailmanager-updater/status.json"),
		UpdateInstalledVersionPath: pathEnv("MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE", "/var/lib/mailmanager-updater/installed-version"),
	}, nil
}

// EnsureDirectories creates only non-secret runtime directories. The key file
// itself is created atomically by cryptox.LoadOrCreateKey.
func (c Config) EnsureDirectories() error {
	for _, dir := range []string{c.DataDir, filepath.Dir(c.DatabasePath), filepath.Dir(c.MasterKeyPath), c.AttachmentCacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func parsePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("MAILMANAGER_PUBLIC_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("MAILMANAGER_PUBLIC_URL must use http or https")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MAILMANAGER_PUBLIC_URL must be an origin without credentials, query, or fragment")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if host != "localhost" {
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return nil, errors.New("MAILMANAGER_PUBLIC_URL must use https unless it is loopback")
			}
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func envOr(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func pathEnv(name, fallback string) string {
	path := envOr(name, fallback)
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func byteSizeEnv(name string, fallback int64) (int64, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}
